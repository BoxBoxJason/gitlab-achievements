// Package backfill walks a GitLab instance's history once, so activity
// that happened before this app existed still earns achievements.
//
// The walk is instance-wide and project-driven: projects are visited in
// ascending ID order, and each one's history is pulled from the Events API
// (commits, merge requests, approvals, issues, notes) plus the Pipelines
// API for what events don't cover. Users are discovered from the activity
// itself rather than enumerated up front, so a user who never did anything
// costs nothing.
//
// Three properties matter more than speed here:
//
//   - One evaluation path. Everything fetched is normalized into
//     activity.Event and handed to the same activity.Processor live webhook
//     ingestion will feed, so historical and live activity are judged by
//     identical rules rather than by two implementations that drift.
//   - Resumability. Progress is persisted as it goes (see progress.go), so
//     an interrupted run resumes near where it stopped instead of starting
//     over, and a finished run is never repeated: the completion watermark
//     survives restarts.
//   - Restraint. This is the heaviest read workload the app ever runs
//     against an instance it doesn't own. Request pacing is applied at the
//     client (see gitlabclient.WithRateLimit), the look-back window is
//     configurable, and a project this app can't read is skipped rather
//     than treated as a fatal error.
package backfill

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

const (
	// pageSize is how many records each API page carries. GitLab caps
	// per_page at 100, and the walk is long enough that fewer, fuller
	// pages measurably reduce total requests.
	pageSize = 100
	// keysetPagination asks GitLab for keyset pagination, which stays
	// cheap arbitrarily deep into a collection where offset pagination
	// degrades. Endpoints that don't support it answer with offset
	// pagination instead, which the client's iterator follows either way.
	keysetPagination = "keyset"
	// progressFlushRecords is how many records may be processed between
	// two cursor writes. Flushing per record would put a database write in
	// front of every event; flushing per project would throw away hours of
	// work on a large project. This bounds re-walked work after an
	// interruption without making the walk write-bound.
	progressFlushRecords = 200
)

// historyReader is the subset of gitlabclient.ReadClient the walk needs.
type historyReader interface {
	ListProjects(opt *gitlab.ListProjectsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Project, error]
	ListProjectVisibleEvents(pid any, opt *gitlab.ListProjectVisibleEventsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.ProjectEvent, error]
	ListProjectPipelines(pid any, opt *gitlab.ListProjectPipelinesOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.PipelineInfo, error]
	GetPipeline(pid any, pipeline int64, options ...gitlab.RequestOptionFunc) (*gitlab.Pipeline, error)
}

// Options tunes a backfill run.
type Options struct {
	// Since bounds how far back the walk reaches. The zero value walks the
	// instance's full history; anything else is passed to GitLab as a
	// server-side filter, so a bounded window costs proportionally fewer
	// requests rather than fetching everything and discarding the excess.
	Since time.Time
	// Logger receives progress and per-project skip reporting. A run this
	// long is otherwise invisible while it happens. May be nil.
	Logger *zap.Logger
	// Force re-walks history even though a completion watermark says it
	// was already done, and starts from the beginning rather than from a
	// saved cursor. Already-processed activity is discarded by the
	// processor, so this costs requests, not correctness.
	Force bool
}

// Report summarizes what a run did.
type Report struct {
	// CompletedAt is when the walk finished; for an AlreadyComplete
	// report, when the earlier walk finished.
	CompletedAt time.Time
	// Projects counts projects walked to completion in this run.
	Projects int
	// ProjectsSkipped counts projects passed over because this app can't
	// read them or they vanished mid-walk.
	ProjectsSkipped int
	// Events counts Events API records processed.
	Events int
	// Pipelines counts pipelines processed.
	Pipelines int
	// Resumed reports whether the run picked up a saved cursor rather than
	// starting from the beginning of the instance.
	Resumed bool
	// AlreadyComplete reports that the run did nothing because history had
	// already been walked end to end.
	AlreadyComplete bool
}

// Run walks the instance's history and feeds it to processor.
//
// It is a no-op when a previous run already completed, unless
// Options.Force says otherwise, which is what makes it safe to call on
// every startup: a pod restart resumes an interrupted walk and skips a
// finished one, with no operator involvement either way.
//
// An interrupted run (cancelled context, GitLab unreachable) returns the
// error with its cursor persisted, so the next call continues rather than
// restarting.
func Run(ctx context.Context, read historyReader, conn *gorm.DB, processor activity.Processor, opts Options) (*Report, error) {
	completedAt, alreadyDone, err := CompletedAt(conn)
	if err != nil {
		return nil, err
	}

	if alreadyDone && !opts.Force {
		return &Report{AlreadyComplete: true, CompletedAt: completedAt}, nil
	}

	saved, err := loadProgress(conn)
	if err != nil {
		return nil, err
	}

	if opts.Force {
		saved = progress{}
	}

	run := &runner{
		read:      read,
		conn:      conn,
		processor: processor,
		opts:      opts,
		logger:    loggerOrNop(opts.Logger),
		progress:  saved,
	}
	run.report.Resumed = saved.LastProjectID > 0 || saved.CurrentProjectID > 0

	err = run.walkProjects(ctx)
	if err != nil {
		return nil, errors.Join(err, run.flush())
	}

	finishedAt := time.Now().UTC()

	err = markCompleted(conn, finishedAt)
	if err != nil {
		return nil, err
	}

	run.report.CompletedAt = finishedAt

	return &run.report, nil
}

// runner carries the state one Run threads through the walk.
type runner struct {
	read      historyReader
	conn      *gorm.DB
	processor activity.Processor
	logger    *zap.Logger
	opts      Options
	progress  progress
	report    Report
	// sinceFlush counts records processed since the cursor was last
	// persisted, bounding how much work an interruption can cost.
	sinceFlush int
}

// walkProjects visits every project the read token can see, in ascending ID
// order, resuming after the last project a previous run finished.
//
// Ascending IDs are what make the cursor a single number: every project at
// or below it is done, and any project created during the walk sorts after
// it and is picked up by the same pass.
func (r *runner) walkProjects(ctx context.Context) error {
	opt := &gitlab.ListProjectsOptions{
		ListOptions: gitlab.ListOptions{
			Pagination: keysetPagination,
			PerPage:    pageSize,
			OrderBy:    "id",
			Sort:       "asc",
		},
		Simple: new(true),
	}

	// Copied, not aliased: the cursor advances as projects finish, and the
	// listing's filter must keep describing where the walk started rather
	// than following it forward and skipping projects on later pages.
	if resumeAfter := r.progress.LastProjectID; resumeAfter > 0 {
		opt.IDAfter = &resumeAfter
	}

	for project, err := range r.read.ListProjects(opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list projects: %w", err)
		}

		err = r.walkProject(ctx, project.ID)
		if err != nil {
			return err
		}
	}

	return nil
}

// walkProject pulls one project's history, in two phases so an interrupted
// project doesn't re-fetch the phase that already finished.
//
// A project this app can't read, or that was deleted between being listed
// and being walked, is skipped rather than failing the run: one
// inaccessible project shouldn't cost the whole instance its backfill.
func (r *runner) walkProject(ctx context.Context, projectID int64) error {
	r.progress.startProject(projectID)

	if r.progress.Phase != phasePipelines {
		err := r.walkProjectEvents(ctx, projectID)
		if err != nil {
			return r.handleProjectError(projectID, "events", err)
		}

		r.progress.Phase = phasePipelines
	}

	err := r.walkProjectPipelines(ctx, projectID)
	if err != nil {
		return r.handleProjectError(projectID, "pipelines", err)
	}

	r.progress.finishProject(projectID)
	r.report.Projects++

	return r.flush()
}

// handleProjectError decides whether a per-project failure ends the run or
// only that project. Anything project-local (gone, or not readable with
// this token) is logged and skipped; anything else, notably a GitLab-wide
// outage or a cancelled context, stops the walk so it can resume later
// instead of racing through the rest of the instance dropping projects.
func (r *runner) handleProjectError(projectID int64, phaseName string, err error) error {
	if !gitlabclient.IsNotFound(err) && !gitlabclient.IsPermissionError(err) {
		return err
	}

	r.logger.Warn("skipping project this app cannot read",
		zap.Int64("project_id", projectID),
		zap.String("phase", phaseName),
		zap.Error(err),
	)

	r.progress.finishProject(projectID)
	r.report.ProjectsSkipped++

	return r.flush()
}

// walkProjectEvents replays a project's Events API history oldest-first.
//
// Oldest-first is what lets the cursor be a date: the walk only ever moves
// forward in time, so resuming means asking for events after the last date
// processed. GitLab's date filters are day-granular, so a resumed walk
// re-reads at most the last day it saw, which the processor discards as
// already-counted.
func (r *runner) walkProjectEvents(ctx context.Context, projectID int64) error {
	opt := &gitlab.ListProjectVisibleEventsOptions{
		ListOptions: gitlab.ListOptions{
			Pagination: keysetPagination,
			PerPage:    pageSize,
			Sort:       "asc",
		},
	}

	floor := r.progress.eventsFloor(r.opts.Since)
	if !floor.IsZero() {
		// GitLab's "after" filter is exclusive of the date given, so the
		// floor day itself is included by asking for the day before it.
		after := gitlab.ISOTime(floor.AddDate(0, 0, -1))
		opt.After = &after
	}

	for event, err := range r.read.ListProjectVisibleEvents(projectID, opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list events for project %d: %w", projectID, err)
		}

		processErr := r.process(ctx, normalizeProjectEvent(event))
		if processErr != nil {
			return processErr
		}

		r.report.Events++
		r.advanceEventsCursor(event)

		flushErr := r.maybeFlush()
		if flushErr != nil {
			return flushErr
		}
	}

	return nil
}

// advanceEventsCursor moves the project's resume point up to the date of
// the event just processed. It never moves backwards, so an instance
// returning events slightly out of order can't rewind the cursor.
func (r *runner) advanceEventsCursor(event *gitlab.ProjectEvent) {
	occurredAt := parseEventTime(event.CreatedAt)
	if occurredAt.IsZero() {
		return
	}

	date := occurredAt.Format(cursorDateLayout)
	if date > r.progress.EventsCursor {
		r.progress.EventsCursor = date
	}
}

// walkProjectPipelines replays a project's pipelines, which the Events API
// doesn't report at all.
//
// The pipeline list omits who triggered each run, so attributing one costs
// a request of its own. That makes this the most expensive part of the
// walk, and the reason the resume cursor tracks pipeline IDs: re-listing a
// page after an interruption is one request, re-fetching its pipelines
// would be a hundred.
func (r *runner) walkProjectPipelines(ctx context.Context, projectID int64) error {
	opt := &gitlab.ListProjectPipelinesOptions{
		ListOptions: gitlab.ListOptions{PerPage: pageSize},
		OrderBy:     new("id"),
		Sort:        new("asc"),
	}

	if !r.opts.Since.IsZero() {
		opt.CreatedAfter = &r.opts.Since
	}

	for info, err := range r.read.ListProjectPipelines(projectID, opt, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list pipelines for project %d: %w", projectID, err)
		}

		if info.ID <= r.progress.LastPipelineID {
			continue
		}

		processErr := r.processPipeline(ctx, projectID, info.ID)
		if processErr != nil {
			return processErr
		}

		r.progress.LastPipelineID = info.ID

		flushErr := r.maybeFlush()
		if flushErr != nil {
			return flushErr
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

	err = r.process(ctx, normalizePipeline(pipeline))
	if err != nil {
		return err
	}

	r.report.Pipelines++

	return nil
}

// process hands normalized activity to the achievement engine.
func (r *runner) process(ctx context.Context, activities []activity.Event) error {
	for _, event := range activities {
		err := r.processor.Process(ctx, event)
		if err != nil {
			return fmt.Errorf("failed to process %s activity %q: %w", event.Kind, event.DedupKey, err)
		}
	}

	return nil
}

// maybeFlush persists the cursor once enough records have gone by since the
// last write.
func (r *runner) maybeFlush() error {
	r.sinceFlush++

	if r.sinceFlush < progressFlushRecords {
		return nil
	}

	return r.flush()
}

// flush persists the cursor a resumed run picks up from.
func (r *runner) flush() error {
	r.sinceFlush = 0

	return saveProgress(r.conn, r.progress)
}

// loggerOrNop returns logger, or a logger that discards everything when
// none was configured, so callers never have to nil-check.
func loggerOrNop(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}

	return logger
}
