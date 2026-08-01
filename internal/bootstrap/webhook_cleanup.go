package bootstrap

import (
	"context"
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// CleanupOptions tunes how thorough RemoveWebhooks is, and whether it
// actually removes anything.
type CleanupOptions struct {
	// Sweep additionally enumerates the instance and deletes any hook
	// pointing at this app's URL, whether or not this app recorded
	// registering it.
	//
	// It exists for the case the recorded hooks cannot cover: a database
	// that was lost, restored from a backup taken before the last
	// registration sweep, or replaced while the hooks on GitLab stayed put.
	// It costs a full enumeration of every group or project on the
	// instance, which is why it is not the default.
	Sweep bool
	// DryRun reports what would be removed without calling GitLab or
	// touching the recorded hooks.
	DryRun bool
}

// CleanupReport summarizes what a removal pass did.
type CleanupReport struct {
	// SweptScopes names the scopes the instance sweep walked, and is empty
	// unless CleanupOptions.Sweep was set.
	SweptScopes []db.HookScope
	// Deleted counts hooks removed from GitLab.
	Deleted int
	// AlreadyGone counts hooks that this app had recorded but that GitLab
	// no longer has: deleted by hand, or attached to a group or project
	// that has since been deleted itself.
	AlreadyGone int
	// Skipped counts hooks left in place because this app's token may not
	// remove them. Their records are kept so a later run with a
	// better-privileged token still knows what to clean up.
	Skipped int
	// Swept counts the groups or projects the instance sweep enumerated.
	Swept int
}

// RemoveWebhooks deletes the event ingestion webhooks this app registered,
// which is what uninstalling it from a GitLab instance amounts to on the
// GitLab side.
//
// The recorded hooks are the source of truth: every hook this app has ever
// registered or adopted has a db.RegisteredHook row naming the group or
// project it sits on and its GitLab ID, so removal is a direct DELETE per
// row rather than a scan of the instance. Rows are dropped as their hooks
// go, which is what makes the pass resumable: an interrupted run leaves
// behind exactly the hooks it had not reached yet.
//
// This is deliberately not something the server does on shutdown. A hook
// deleted on SIGTERM would be re-registered on the next start, so every
// restart, rollout, and pod eviction would tear down and rebuild every hook
// on the instance, losing the events that arrive in between. Removal is an
// operator's decision to stop running this app, not a lifecycle event.
//
// Note that hooks removed while a server is still running are registered
// again by its next reconciliation sweep. Stop the deployment first.
func RemoveWebhooks(
	ctx context.Context,
	read hookTargetLister,
	write hookManager,
	conn *gorm.DB,
	cfg *config.Config,
	webhookURL string,
	opts CleanupOptions,
) (CleanupReport, error) {
	cleanup := &hookCleanup{
		read:       read,
		write:      write,
		conn:       conn,
		webhookURL: webhookURL,
		limiter:    sweepLimiter(cfg.HookRate),
		dryRun:     opts.DryRun,
	}

	err := cleanup.removeRecorded(ctx)
	if err != nil {
		return CleanupReport{}, err
	}

	if opts.Sweep {
		err = cleanup.sweepInstance(ctx, config.HookScope(cfg.HookScope))
		if err != nil {
			return CleanupReport{}, err
		}
	}

	return cleanup.report, nil
}

// hookCleanup carries the state one removal pass threads through its work.
type hookCleanup struct {
	read       hookTargetLister
	write      hookManager
	conn       *gorm.DB
	limiter    *rate.Limiter
	webhookURL string
	report     CleanupReport
	dryRun     bool
}

// removeRecorded deletes every hook this app recorded registering.
//
// The rows are walked in batches rather than loaded whole because on the
// project scope there is one per project on the instance, and an uninstall
// should not need to hold all of them in memory at once. Deleting rows as
// it goes is safe under GORM's batching, which pages by primary key rather
// than by offset.
func (c *hookCleanup) removeRecorded(ctx context.Context) error {
	var batch []db.RegisteredHook

	err := c.conn.WithContext(ctx).
		Model(&db.RegisteredHook{}).
		FindInBatches(&batch, hookPageSize, func(_ *gorm.DB, _ int) error {
			for _, recorded := range batch {
				target := hookTargetFor(c.write, recorded.Scope, recorded.TargetID)

				paceErr := paceSweep(ctx, c.limiter)
				if paceErr != nil {
					return paceErr
				}

				removeErr := c.removeHook(ctx, target, recorded.HookID)
				if removeErr != nil {
					return removeErr
				}
			}

			return nil
		}).Error
	if err != nil {
		return fmt.Errorf("failed to remove the recorded hooks: %w", err)
	}

	return nil
}

// sweepInstance walks the instance looking for hooks pointing at this app's
// URL that removeRecorded had no record of, and removes those too.
func (c *hookCleanup) sweepInstance(ctx context.Context, configured config.HookScope) error {
	scopes, err := c.cleanupScopes(ctx, configured)
	if err != nil {
		return err
	}

	c.report.SweptScopes = scopes

	for _, scope := range scopes {
		if scope == db.HookScopeGroup {
			err = c.sweepGroups(ctx)
		} else {
			err = c.sweepProjects(ctx)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// cleanupScopes decides which kinds of object the sweep looks at.
//
// An explicitly configured scope is honored as-is, matching what
// resolveHookScope does for registration: an operator who pinned a scope
// gets a sweep over exactly that one.
//
// Under the default "auto" the answer is both, because both are things
// this app could have registered on this instance. Project hooks are what
// auto falls back to whenever group hooks are unavailable, including
// during an earlier run when the instance's license was different, so a
// group-scoped instance is still swept for project hooks left over from
// before. The reverse does not hold: an instance where group hooks do not
// exist has never had one registered, so that pass is skipped.
func (c *hookCleanup) cleanupScopes(ctx context.Context, configured config.HookScope) ([]db.HookScope, error) {
	resolved, err := resolveHookScope(ctx, c.write, configured)
	if err != nil {
		return nil, err
	}

	if configured != config.HookScopeAuto {
		return []db.HookScope{resolved}, nil
	}

	if resolved == db.HookScopeGroup {
		return []db.HookScope{db.HookScopeGroup, db.HookScopeProject}, nil
	}

	return []db.HookScope{db.HookScopeProject}, nil
}

// sweepGroups looks for leftover hooks on every top-level group, the same
// set syncGroups registers them on.
func (c *hookCleanup) sweepGroups(ctx context.Context) error {
	opt := &gitlab.ListGroupsOptions{
		ListOptions:  gitlab.ListOptions{Pagination: keysetPagination, PerPage: hookPageSize},
		TopLevelOnly: new(true),
		AllAvailable: new(true),
	}

	for group, err := range c.read.ListGroups(opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list top-level groups: %w", err)
		}

		err = c.sweepTarget(ctx, &groupHookTarget{write: c.write, groupID: group.ID})
		if err != nil {
			return err
		}
	}

	return nil
}

// sweepProjects looks for leftover hooks on every project syncProjects
// would have registered one on.
func (c *hookCleanup) sweepProjects(ctx context.Context) error {
	opt := &gitlab.ListProjectsOptions{
		ListOptions: gitlab.ListOptions{
			Pagination: keysetPagination,
			PerPage:    hookPageSize,
			OrderBy:    "id",
			Sort:       "asc",
		},
		Simple: new(true),
	}

	for project, err := range c.read.ListProjects(opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list projects: %w", err)
		}

		if !gitlabclient.ProjectOwnedByGroup(project) {
			continue
		}

		err = c.sweepTarget(ctx, &projectHookTarget{write: c.write, projectID: project.ID})
		if err != nil {
			return err
		}
	}

	return nil
}

// sweepTarget removes every hook on one group or project that points at
// this app's URL. The URL is the only thing identifying a hook as this
// app's here, exactly as it is in recoverHook's adoption scan: names can be
// edited and GitLab never returns the token.
func (c *hookCleanup) sweepTarget(ctx context.Context, target hookTarget) error {
	err := paceSweep(ctx, c.limiter)
	if err != nil {
		return err
	}

	c.report.Swept++

	existing, err := target.list(ctx)
	if err != nil {
		// A target that vanished between being enumerated and being read
		// took its hooks with it, which is the outcome this wants anyway.
		if gitlabclient.IsNotFound(err) {
			return nil
		}

		return c.handleTargetError(target, fmt.Errorf("failed to list hooks: %w", err))
	}

	for _, hook := range existing {
		if hook.url != c.webhookURL {
			continue
		}

		err = c.removeHook(ctx, target, hook.id)
		if err != nil {
			return err
		}
	}

	return nil
}

// removeHook deletes one hook and forgets the record of it.
//
// A hook GitLab no longer has counts as gone rather than as a failure:
// somebody deleting it by hand, or deleting the project it sat on, is the
// outcome this is trying to reach anyway. A hook this app's token may not
// touch is left in place with its record intact, so that re-running with a
// better-privileged token still knows where it is.
func (c *hookCleanup) removeHook(ctx context.Context, target hookTarget, hookID int64) error {
	if c.dryRun {
		c.report.Deleted++

		zap.L().Info("would remove hook",
			zap.String("scope", string(target.scope())),
			zap.Int64("target_id", target.id()),
			zap.Int64("hook_id", hookID),
		)

		return nil
	}

	err := target.remove(ctx, hookID)

	switch {
	case err == nil:
		c.report.Deleted++
	case gitlabclient.IsNotFound(err):
		c.report.AlreadyGone++
	default:
		return c.handleTargetError(target, err)
	}

	return c.forget(target)
}

// forget drops the record of a hook that is no longer on GitLab.
func (c *hookCleanup) forget(target hookTarget) error {
	err := c.conn.
		Where("scope = ? AND target_id = ?", target.scope(), target.id()).
		Delete(&db.RegisteredHook{}).Error
	if err != nil {
		return fmt.Errorf("failed to forget the %s %d hook: %w", target.scope(), target.id(), err)
	}

	return nil
}

// handleTargetError decides whether a per-target failure ends the pass or
// only that target, on the same terms as the registration sweep's
// equivalent: a token that may not manage one group or project shouldn't
// cost the rest of the instance its cleanup, but a GitLab-wide failure
// must not be quietly counted as hundreds of skipped targets.
func (c *hookCleanup) handleTargetError(target hookTarget, err error) error {
	if !gitlabclient.IsPermissionError(err) {
		return fmt.Errorf("failed to remove %s %d hook: %w", target.scope(), target.id(), err)
	}

	zap.L().Warn("leaving behind a hook this app cannot remove",
		zap.String("scope", string(target.scope())),
		zap.Int64("target_id", target.id()),
		zap.Error(err),
	)

	c.report.Skipped++

	return nil
}
