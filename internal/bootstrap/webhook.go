package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

const (
	gitlabAchievementsWebhookName = "gitlab-achievements"

	// hookPageSize is how many records each group/project listing page
	// carries. GitLab caps per_page at 100, and an instance-wide sweep is
	// long enough that fewer, fuller pages measurably reduce total requests.
	hookPageSize = 100
	// keysetPagination asks GitLab for keyset pagination, which stays cheap
	// arbitrarily deep into a collection where offset pagination degrades.
	keysetPagination = "keyset"
	// hookSweepBurst lets a sweep start at full speed before the rate cap
	// takes hold, so a small instance finishes in one breath rather than
	// being paced as though it were a large one.
	hookSweepBurst = 10
)

// paidPlans are the license plans on which group webhooks exist. Anything
// else (Free, CE, or an instance with no license at all) has to fall back
// to per-project hooks.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var paidPlans = map[string]bool{
	"premium":  true,
	"ultimate": true,
}

// hookTargetLister is the subset of gitlabclient.ReadClient hook
// synchronization needs to enumerate what to register hooks on.
type hookTargetLister interface {
	ListGroups(opt *gitlab.ListGroupsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Group, error]
	ListProjects(opt *gitlab.ListProjectsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Project, error]
}

// licenseReader reports the instance's license, which decides whether group
// webhooks are available.
type licenseReader interface {
	GetLicense(options ...gitlab.RequestOptionFunc) (*gitlab.License, error)
}

// groupHookManager is the subset of gitlabclient.WriteClient group hook
// synchronization needs.
type groupHookManager interface {
	ListGroupHooks(gid any, opt *gitlab.ListGroupHooksOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.GroupHook, error)
	AddGroupHook(gid any, opt *gitlab.AddGroupHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.GroupHook, error)
	EditGroupHook(gid any, hook int64, opt *gitlab.EditGroupHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.GroupHook, error)
}

// projectHookManager is the subset of gitlabclient.WriteClient project hook
// synchronization needs.
type projectHookManager interface {
	ListProjectHooks(pid any, opt *gitlab.ListProjectHooksOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.ProjectHook, error)
	AddProjectHook(pid any, opt *gitlab.AddProjectHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.ProjectHook, error)
	EditProjectHook(pid any, hook int64, opt *gitlab.EditProjectHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.ProjectHook, error)
}

// hookManager is everything hook synchronization needs from the write
// client.
type hookManager interface {
	licenseReader
	groupHookManager
	projectHookManager
}

// WebhookReport summarizes what a hook synchronization pass did.
type WebhookReport struct {
	// Scope is the strategy that was actually used, which for the default
	// "auto" configuration is whichever one the instance's license allows.
	Scope db.HookScope
	// Targets counts groups (or projects) whose hook is now registered and
	// carries the desired configuration.
	Targets int
	// Created counts hooks that did not exist and were registered.
	Created int
	// Updated counts hooks that already existed and had their configuration
	// re-applied.
	Updated int
	// Skipped counts targets passed over because this app cannot manage
	// hooks on them, or that vanished mid-sweep.
	Skipped int
}

// ReconcileWebhooks re-applies the same idempotent sweep syncHooks performs
// at bootstrap. It is safe to call repeatedly, e.g. from a periodic job, to
// heal hooks that were altered or deleted after bootstrap ran and to
// register hooks on groups and projects created since.
//
// Discovering new targets is the reason this sweep costs a full enumeration
// every time rather than only touching what it already knows about: GitLab
// delivers project_create and group_create only to system hooks, whose
// event set is too narrow to build on (see the README's webhook strategy),
// so a target created after bootstrap is only picked up here.
func ReconcileWebhooks(
	ctx context.Context,
	read hookTargetLister,
	write hookManager,
	conn *gorm.DB,
	cfg *config.Config,
	webhookURL string,
	logger *zap.Logger,
) (WebhookReport, error) {
	return syncHooks(ctx, read, write, conn, cfg, webhookURL, logger)
}

// syncHooks idempotently registers the webhooks this app ingests events
// from, across the whole instance.
//
// Which objects carry those hooks depends on what the instance's license
// allows (see resolveHookScope): one hook per top-level group where group
// webhooks exist, one hook per project otherwise. Either way the coverage is
// the whole instance, minus projects in personal namespaces (see
// ownedByGroup).
//
// The achievements namespace has nothing to do with any of this. It is only
// where the achievement definitions live and are awarded from; hooks follow
// activity, which happens instance-wide.
//
// A target this app cannot manage hooks on is skipped rather than failing
// the run: one inaccessible project shouldn't cost the whole instance its
// event ingestion.
func syncHooks(
	ctx context.Context,
	read hookTargetLister,
	write hookManager,
	conn *gorm.DB,
	cfg *config.Config,
	webhookURL string,
	logger *zap.Logger,
) (WebhookReport, error) {
	scope, err := resolveHookScope(ctx, write, config.HookScope(cfg.HookScope))
	if err != nil {
		return WebhookReport{}, err
	}

	sync := &hookSync{
		read:       read,
		write:      write,
		conn:       conn,
		webhookURL: webhookURL,
		secret:     cfg.WebhookSecret,
		logger:     loggerOrNop(logger),
		limiter:    sweepLimiter(cfg.HookRate),
		report:     WebhookReport{Scope: scope},
	}

	err = sync.run(ctx, scope)
	if err != nil {
		return WebhookReport{}, err
	}

	return sync.report, nil
}

// resolveHookScope decides which kind of hook to register.
//
// An explicitly configured scope is honored as-is, including when it asks
// for group hooks on an instance that turns out not to support them: the
// resulting failure names the real problem, which is more use to an
// operator than this silently doing something other than what they asked.
//
// Otherwise the instance's license decides. /license is admin-only and does
// not exist at all on Community Edition, so a 404 or a permission failure
// is read as "no group webhooks here" rather than as a fatal error: project
// hooks work on every tier, making them the safe answer whenever the tier
// can't be established.
func resolveHookScope(ctx context.Context, write licenseReader, configured config.HookScope) (db.HookScope, error) {
	switch configured {
	case config.HookScopeGroup:
		return db.HookScopeGroup, nil
	case config.HookScopeProject:
		return db.HookScopeProject, nil
	case config.HookScopeAuto:
	default:
		return "", fmt.Errorf("unknown hook scope %q", configured)
	}

	license, err := write.GetLicense(gitlab.WithContext(ctx))
	if err != nil {
		if gitlabclient.IsNotFound(err) || gitlabclient.IsPermissionError(err) {
			return db.HookScopeProject, nil
		}

		return "", fmt.Errorf("failed to determine instance license, pass --hook-scope to select a webhook strategy explicitly: %w", err)
	}

	if license != nil && paidPlans[license.Plan] {
		return db.HookScopeGroup, nil
	}

	return db.HookScopeProject, nil
}

// hookSync carries the state one synchronization pass threads through the
// sweep.
type hookSync struct {
	read  hookTargetLister
	write hookManager
	conn  *gorm.DB
	// limiter paces the sweep. Unlike the backfill, which reads through a
	// rate-capped client of its own, the clients here are shared with the
	// readiness probe and with achievement awarding, so the cap belongs on
	// the workload rather than on the connection. May be nil, which paces
	// nothing.
	limiter    *rate.Limiter
	logger     *zap.Logger
	webhookURL string
	secret     string
	report     WebhookReport
}

// run sweeps every target the chosen scope covers.
func (s *hookSync) run(ctx context.Context, scope db.HookScope) error {
	if scope == db.HookScopeGroup {
		return s.syncGroups(ctx)
	}

	return s.syncProjects(ctx)
}

// syncGroups registers a hook on every top-level group on the instance.
//
// Only top-level groups are visited because a group hook already covers the
// whole subtree beneath it: descending into subgroups would register a
// redundant second hook on everything below them, and deliver every event
// twice.
func (s *hookSync) syncGroups(ctx context.Context) error {
	opt := &gitlab.ListGroupsOptions{
		ListOptions:  gitlab.ListOptions{Pagination: keysetPagination, PerPage: hookPageSize},
		TopLevelOnly: new(true),
		AllAvailable: new(true),
	}

	for group, err := range s.read.ListGroups(opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list top-level groups: %w", err)
		}

		err = s.syncTarget(ctx, &groupHookTarget{write: s.write, groupID: group.ID})
		if err != nil {
			return err
		}
	}

	return nil
}

// syncProjects registers a hook on every project on the instance, which is
// what the project scope has to cover: hooks follow activity, and activity
// happens in every project, not only the ones under some particular group.
//
// This deliberately walks projects directly rather than group by group. The
// two would cover the same projects, but the flat walk is one enumeration
// instead of one per group, doesn't depend on the read token being able to
// see every group on the instance, and covers exactly what the historical
// backfill walks, which is what keeps the two paths from disagreeing about
// whose activity counts.
//
// Projects in personal namespaces are the one exclusion (see
// ownedByGroup).
func (s *hookSync) syncProjects(ctx context.Context) error {
	opt := &gitlab.ListProjectsOptions{
		ListOptions: gitlab.ListOptions{
			Pagination: keysetPagination,
			PerPage:    hookPageSize,
			OrderBy:    "id",
			Sort:       "asc",
		},
		Simple: new(true),
	}

	for project, err := range s.read.ListProjects(opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list projects: %w", err)
		}

		if !gitlabclient.ProjectOwnedByGroup(project) {
			continue
		}

		err = s.syncTarget(ctx, &projectHookTarget{write: s.write, projectID: project.ID})
		if err != nil {
			return err
		}
	}

	return nil
}

// syncTarget brings one group's or project's hook in line with the desired
// configuration, and records its ID so the next pass can look it up
// directly.
//
// The stored ID is what keeps a sweep cheap: the common case is one
// GetHook per target rather than a ListHooks scan. The three cases are the
// same ones any idempotent registration faces:
//
//   - stored ID found and the hook still exists: EditHook unconditionally
//     re-applies the desired URL, token, and event set, healing any drift.
//   - stored ID found but the hook 404s: it was deleted out of band; fall
//     through to the recovery path.
//   - no stored ID (first sweep, a target created since the last one, or an
//     upgrade from a version that stored none): scan the target's hooks for
//     one already pointing at this app's URL, adopting it if found, and
//     register a new one otherwise.
//
// A transient failure is returned as-is rather than treated as "deleted",
// so a hiccup can't leave a second hook registered on every retry.
func (s *hookSync) syncTarget(ctx context.Context, target hookTarget) error {
	err := s.pace(ctx)
	if err != nil {
		return err
	}

	storedID, found, err := loadHookID(s.conn, target.scope(), target.id())
	if err != nil {
		return err
	}

	if found {
		hookID, ok, reuseErr := s.reuseStoredHook(ctx, target, storedID)
		if reuseErr != nil {
			return s.handleTargetError(target, reuseErr)
		}

		if ok {
			s.report.Targets++
			s.report.Updated++

			return storeHookID(s.conn, target.scope(), target.id(), hookID)
		}
	}

	created, err := s.recoverHook(ctx, target)
	if err != nil {
		return s.handleTargetError(target, err)
	}

	s.report.Targets++

	if created {
		s.report.Created++
	} else {
		s.report.Updated++
	}

	return nil
}

// sweepLimiter builds the sweep's rate cap, or none at all when the
// configured rate is not positive.
//
// Config validation rejects a non-positive rate, so that case is a caller
// that skipped it. Running unpaced is the honest answer there: it is what
// this sweep did before it was capped, where a rate.Limit of 0 would
// instead admit the first burst and then block forever.
func sweepLimiter(perSecond float64) *rate.Limiter {
	if perSecond <= 0 {
		return nil
	}

	return rate.NewLimiter(rate.Limit(perSecond), hookSweepBurst)
}

// pace waits for the sweep's rate cap to admit the next target.
//
// The cap is applied per target rather than per request because that is
// what the sweep's cost is proportional to: one or two calls each, over
// every group or project on the instance, every hour for as long as the
// app runs. Enumerating the targets is left unpaced, being one request per
// hundred of them.
func (s *hookSync) pace(ctx context.Context) error {
	if s.limiter == nil {
		return nil
	}

	err := s.limiter.Wait(ctx)
	if err != nil {
		return fmt.Errorf("hook sweep interrupted while pacing: %w", err)
	}

	return nil
}

// reuseStoredHook re-applies the desired configuration to the hook
// identified by storedID. The second return value is false only when the
// hook was confirmed deleted (404), signaling the caller should fall back
// to recoverHook.
//
// The edit is issued straight off the stored ID, with no read to confirm
// the hook is still there first: the edit 404s on a deleted hook just as a
// read would, so the read only ever bought a second request per target on
// every sweep. That matters at this scale, where the sweep touches every
// project on the instance every hour.
//
// It is also unconditional. GitLab never returns a hook's token, so there
// is no reading the remote state and concluding it already matches: a
// rotated secret would be invisible, and the hooks would keep presenting
// the old one until something forced an edit. Re-applying every time is
// what makes drift and rotation heal on the same sweep.
func (s *hookSync) reuseStoredHook(ctx context.Context, target hookTarget, storedID int64) (int64, bool, error) {
	hookID, err := target.edit(ctx, storedID, s.webhookURL, s.secret)

	switch {
	case err == nil:
		return hookID, true, nil
	case errors.Is(err, gitlab.ErrNotFound):
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("failed to update stored hook %d: %w", storedID, err)
	}
}

// recoverHook is the fallback used when no hook ID is stored yet or the
// stored one was confirmed deleted: it scans the target's existing hooks by
// URL before registering a new one, then persists whichever ID it lands on.
// It reports whether a new hook had to be created.
func (s *hookSync) recoverHook(ctx context.Context, target hookTarget) (bool, error) {
	existing, err := target.list(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to list hooks: %w", err)
	}

	for _, hook := range existing {
		if hook.url != s.webhookURL {
			continue
		}

		hookID, editErr := target.edit(ctx, hook.id, s.webhookURL, s.secret)
		if editErr != nil {
			return false, fmt.Errorf("failed to update existing hook %d: %w", hook.id, editErr)
		}

		return false, storeHookID(s.conn, target.scope(), target.id(), hookID)
	}

	hookID, err := target.add(ctx, s.webhookURL, s.secret)
	if err != nil {
		return false, fmt.Errorf("failed to register hook: %w", err)
	}

	return true, storeHookID(s.conn, target.scope(), target.id(), hookID)
}

// handleTargetError decides whether a per-target failure ends the sweep or
// only that target. Anything target-local (gone, or not manageable with
// this token) is logged and skipped; anything else, notably a GitLab-wide
// outage or a cancelled context, stops the sweep rather than racing through
// the rest of the instance leaving it unhooked.
func (s *hookSync) handleTargetError(target hookTarget, err error) error {
	if !s.skippable(err) {
		return fmt.Errorf("failed to sync %s %d hook: %w", target.scope(), target.id(), err)
	}

	s.logger.Warn("skipping target this app cannot manage hooks on",
		zap.String("scope", string(target.scope())),
		zap.Int64("target_id", target.id()),
		zap.Error(err),
	)
	s.report.Skipped++

	return nil
}

// skippable reports whether err concerns only the target it came from.
func (s *hookSync) skippable(err error) bool {
	return gitlabclient.IsNotFound(err) || gitlabclient.IsPermissionError(err)
}

// loadHookID reads the previously persisted hook ID for one target, if any.
func loadHookID(conn *gorm.DB, scope db.HookScope, targetID int64) (int64, bool, error) {
	var hook db.RegisteredHook

	err := conn.Where("scope = ? AND target_id = ?", scope, targetID).First(&hook).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("failed to load stored %s %d hook id: %w", scope, targetID, err)
	}

	return hook.HookID, true, nil
}

// storeHookID persists hookID as the hook currently registered on one
// target, creating the row on first use and overwriting it thereafter.
func storeHookID(conn *gorm.DB, scope db.HookScope, targetID, hookID int64) error {
	record := db.RegisteredHook{Scope: scope, TargetID: targetID}

	err := conn.Where(&db.RegisteredHook{Scope: scope, TargetID: targetID}).
		Attrs(&db.RegisteredHook{HookID: hookID}).
		FirstOrCreate(&record).Error
	if err != nil {
		return fmt.Errorf("failed to persist %s %d hook id: %w", scope, targetID, err)
	}

	if record.HookID != hookID {
		err = conn.Model(&record).Update("hook_id", hookID).Error
		if err != nil {
			return fmt.Errorf("failed to update %s %d hook id: %w", scope, targetID, err)
		}
	}

	return nil
}

// loggerOrNop returns logger, or one that discards everything when none was
// configured, so callers never have to nil-check.
func loggerOrNop(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}

	return logger
}
