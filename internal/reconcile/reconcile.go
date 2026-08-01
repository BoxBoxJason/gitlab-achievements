// Package reconcile re-reads a recent window of instance activity through
// the Events API, so the app stays correct when individual webhook
// deliveries never arrive.
//
// Webhooks are best-effort: GitLab retries a delivery a few times and then
// gives up, and a deploy, a network blip, or an instance restart is enough
// to lose one for good. Nothing in the live path notices, because a
// delivery that never arrives leaves no trace to notice. This walk is the
// safety net: it asks GitLab what actually happened over the last day or
// two and feeds it back through the achievement engine, which counts what
// was missed and discards everything else.
//
// # Why it does not double-count
//
// Everything here is normalized by the eventsapi package, which derives the
// same dedup keys the webhook path derives for the same activity. So the
// overwhelming majority of what a pass reads is activity the live path
// already counted, and the engine's processed-event log discards all of it.
// That property is the whole basis of this package: without it, re-reading
// a window webhooks already covered would inflate every counter in it, and
// GitLab awards are never revoked, so the inflation would be permanent.
//
// The Events API reports pushes, merge requests, issues and comments, and
// the Pipelines API covers pipelines. Jobs, deployments, emoji reactions,
// wiki pages, resolved discussions and fast merges have no read-side
// representation at all, so a lost delivery of one of those stays lost.
// That is the safe direction: this heals what it can see and undercounts
// the rest, rather than guessing.
//
// # Cost
//
// A pass costs roughly one request per project with activity in the
// window, plus pagination and a request per pipeline it has to attribute.
// Projects are narrowed server-side by last_activity_after, so a quiet
// instance costs almost nothing however many projects it holds, and a busy
// one pays in proportion to how busy it was rather than to its size. The
// read client is expected to be rate-limited by the caller for the same
// reason the backfill's is.
package reconcile

import (
	"context"
	"fmt"
	"iter"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/eventsapi"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

const (
	// pageSize is how many records each API page carries; GitLab caps
	// per_page at 100.
	pageSize = 100
	// keysetPagination asks GitLab for keyset pagination, which stays cheap
	// arbitrarily deep into a collection. Endpoints that don't support it
	// answer with offset pagination instead, which the client's iterator
	// follows either way.
	keysetPagination = "keyset"
)

// activityReader is the subset of gitlabclient.ReadClient a pass needs.
type activityReader interface {
	ListProjects(opt *gitlab.ListProjectsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Project, error]
	ListProjectVisibleEvents(pid any, opt *gitlab.ListProjectVisibleEventsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.ProjectEvent, error]
	ListProjectPipelines(pid any, opt *gitlab.ListProjectPipelinesOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.PipelineInfo, error]
	GetPipeline(pid any, pipeline int64, options ...gitlab.RequestOptionFunc) (*gitlab.Pipeline, error)
}

// Options tunes a reconciliation pass.
type Options struct {
	// Now overrides the clock, for tests. The zero value uses time.Now.
	Now func() time.Time
	// Lookback is the minimum window a pass covers, counted back from the
	// moment it starts. It should comfortably exceed the interval between
	// passes so that consecutive windows overlap rather than abut: a window
	// that merely met the previous one would lose anything GitLab recorded
	// with a timestamp on the wrong side of the boundary.
	Lookback time.Duration
}

// Report summarizes what a pass did.
type Report struct {
	// Since and Until bound the window the pass actually covered.
	Since time.Time
	Until time.Time
	// Projects counts projects whose recent activity was re-read.
	Projects int
	// ProjectsSkipped counts projects passed over because this app can't
	// read them or they vanished mid-pass.
	ProjectsSkipped int
	// Events counts Events API records re-read, whether or not they turned
	// out to be new. Most of them will not be: see the package doc.
	Events int
	// Pipelines counts pipelines re-read.
	Pipelines int
	// Gap is how long it had been since the last successful pass, beyond
	// the interval one is expected to take. A non-zero value means passes
	// were being missed, which is what makes a lost webhook window
	// visible rather than silent.
	Gap time.Duration
}

// Run re-reads the recent activity window and feeds it to processor.
//
// The window starts at whichever is earlier: Options.Lookback before now,
// or far enough back to cover everything since the last successful pass.
// The second case is what makes an outage self-healing — an app down for a
// week comes back and re-reads the week — and it is bounded only by how
// long it was down, so a long gap costs a long pass. The window's width is
// reported, and logged by the caller, for exactly that reason.
//
// The watermark advances only on success. A pass that fails halfway leaves
// it alone, so the next one re-covers the same window rather than stepping
// over the half that was never read.
func Run(ctx context.Context, read activityReader, conn *gorm.DB, processor activity.Processor, opts Options) (*Report, error) {
	startedAt := opts.now()

	since, gap, err := window(conn, startedAt, opts.Lookback)
	if err != nil {
		return nil, err
	}

	run := &runner{
		read:      read,
		processor: processor,
		since:     since,
		report:    Report{Since: since, Until: startedAt, Gap: gap},
	}

	err = run.walkProjects(ctx)
	if err != nil {
		return nil, err
	}

	err = markCompleted(conn, startedAt)
	if err != nil {
		return nil, err
	}

	return &run.report, nil
}

// runner carries the state one Run threads through the pass.
type runner struct {
	read      activityReader
	processor activity.Processor
	since     time.Time
	report    Report
}

// walkProjects visits every project that has seen activity since the
// window opened.
//
// The narrowing is server-side, through last_activity_after, which is what
// makes a daily pass affordable on an instance with thousands of projects:
// without it every pass would cost a request per project whether or not
// anybody touched it. GitLab bumps last_activity_at on the activity this
// app tracks, so a project excluded by the filter has nothing to heal;
// where an instance disagrees, the cost is a missed heal in the window,
// which the live path had already handled in all but the failure case this
// exists for.
func (r *runner) walkProjects(ctx context.Context) error {
	opt := &gitlab.ListProjectsOptions{
		ListOptions: gitlab.ListOptions{
			Pagination: keysetPagination,
			PerPage:    pageSize,
			OrderBy:    "id",
			Sort:       "asc",
		},
		LastActivityAfter: &r.since,
		Simple:            new(true),
	}

	for project, err := range r.read.ListProjects(opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list recently active projects: %w", err)
		}

		// Skipped for the same reason the webhook sweep registers no hook
		// on them and the historical walk passes them over: a project live
		// ingestion cannot reach is not one this app awards for.
		if !gitlabclient.ProjectOwnedByGroup(project) {
			r.report.ProjectsSkipped++

			continue
		}

		err = r.walkProject(ctx, project.ID)
		if err != nil {
			return err
		}
	}

	return nil
}

// walkProject re-reads one project's recent events and pipelines.
//
// A project this app can't read, or that was deleted between being listed
// and being walked, is skipped rather than failing the pass: one
// inaccessible project shouldn't cost the instance its reconciliation, and
// unlike the historical walk there is no cursor to lose by carrying on.
func (r *runner) walkProject(ctx context.Context, projectID int64) error {
	err := r.walkProjectEvents(ctx, projectID)
	if err != nil {
		return r.handleProjectError(projectID, "events", err)
	}

	err = r.walkProjectPipelines(ctx, projectID)
	if err != nil {
		return r.handleProjectError(projectID, "pipelines", err)
	}

	r.report.Projects++

	return nil
}

// handleProjectError decides whether a per-project failure ends the pass or
// only that project. Anything project-local (gone, or not readable with
// this token) is logged and skipped; anything else, notably a GitLab-wide
// outage or a cancelled context, stops the pass so the watermark stays put
// and the next one covers the same window.
func (r *runner) handleProjectError(projectID int64, phase string, err error) error {
	if !gitlabclient.IsNotFound(err) && !gitlabclient.IsPermissionError(err) {
		return err
	}

	zap.L().Warn("skipping project this app cannot read",
		zap.Int64("project_id", projectID),
		zap.String("phase", phase),
		zap.Error(err),
	)

	r.report.ProjectsSkipped++

	return nil
}

// walkProjectEvents re-reads a project's events over the window.
//
// GitLab's date filters are day-granular, so the floor is asked for a day
// early: the "after" filter excludes the date given, and a window opening
// partway through a day would otherwise drop that day's earlier half. The
// excess is re-read, not re-counted.
func (r *runner) walkProjectEvents(ctx context.Context, projectID int64) error {
	after := gitlab.ISOTime(r.since.AddDate(0, 0, -1))

	opt := &gitlab.ListProjectVisibleEventsOptions{
		ListOptions: gitlab.ListOptions{
			Pagination: keysetPagination,
			PerPage:    pageSize,
			Sort:       "asc",
		},
		After: &after,
	}

	for event, err := range r.read.ListProjectVisibleEvents(projectID, opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list events for project %d: %w", projectID, err)
		}

		processErr := r.process(ctx, eventsapi.NormalizeProjectEvent(event))
		if processErr != nil {
			return processErr
		}

		r.report.Events++
	}

	return nil
}

// walkProjectPipelines re-reads a project's recent pipelines, which the
// Events API doesn't report at all.
//
// Pipelines are selected by when they were last updated rather than when
// they were created, because a pipeline's outcome is what the criteria turn
// on: one started before the window opened and finishing inside it is
// exactly the case a lost delivery would have cost, and filtering on
// creation would miss it.
func (r *runner) walkProjectPipelines(ctx context.Context, projectID int64) error {
	opt := &gitlab.ListProjectPipelinesOptions{
		ListOptions:  gitlab.ListOptions{PerPage: pageSize},
		UpdatedAfter: &r.since,
		OrderBy:      new("id"),
		Sort:         new("asc"),
	}

	for info, err := range r.read.ListProjectPipelines(projectID, opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list pipelines for project %d: %w", projectID, err)
		}

		processErr := r.processPipeline(ctx, projectID, info.ID)
		if processErr != nil {
			return processErr
		}
	}

	return nil
}

// processPipeline fetches the detail a pipeline's activity needs (the
// listing carries no user) and processes it. A pipeline deleted between
// being listed and being fetched is skipped, not fatal.
func (r *runner) processPipeline(ctx context.Context, projectID, pipelineID int64) error {
	pipeline, err := r.read.GetPipeline(projectID, pipelineID, gitlab.WithContext(ctx))
	if err != nil {
		if gitlabclient.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to get pipeline %d of project %d: %w", pipelineID, projectID, err)
	}

	err = r.process(ctx, eventsapi.NormalizePipeline(pipeline))
	if err != nil {
		return err
	}

	r.report.Pipelines++

	return nil
}

// process hands normalized activity to the achievement engine, which
// discards whatever it has already counted.
func (r *runner) process(ctx context.Context, activities []activity.Event) error {
	for _, event := range activities {
		err := r.processor.Process(ctx, event)
		if err != nil {
			return fmt.Errorf("failed to process %s activity %q: %w", event.Kind, event.DedupKey, err)
		}
	}

	return nil
}

// now reads the clock the options select.
func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}

	return time.Now()
}
