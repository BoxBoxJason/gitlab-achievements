package reconcile

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// startedAt is the fixed "now" every pass in these tests runs at, so the
// windows they ask GitLab for are assertable.
//
//nolint:gochecknoglobals // a test fixture, read-only
var startedAt = time.Date(2026, time.March, 4, 12, 0, 0, 0, time.UTC)

const testLookback = 48 * time.Hour

func testConn(t *testing.T) *gorm.DB {
	t.Helper()

	conn, err := appdb.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory test database: %v", err)
	}

	if err := appdb.Migrate(conn); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}

	return conn
}

func groupProject(id int64) *gitlab.Project {
	return &gitlab.Project{
		ID:        id,
		Namespace: &gitlab.ProjectNamespace{ID: 100 + id, Kind: "group"},
	}
}

func userProject(id int64) *gitlab.Project {
	return &gitlab.Project{
		ID:        id,
		Namespace: &gitlab.ProjectNamespace{ID: 100 + id, Kind: "user"},
	}
}

// fakeReader serves a canned instance to a pass and records what it was
// asked for, so tests can assert on the window requested as well as on the
// activity produced.
type fakeReader struct {
	projects  []*gitlab.Project
	events    map[int64][]*gitlab.ProjectEvent
	pipelines map[int64][]*gitlab.PipelineInfo
	details   map[int64]*gitlab.Pipeline

	projectsErr  error
	eventsErr    map[int64]error
	pipelinesErr map[int64]error

	projectsOpt  gitlab.ListProjectsOptions
	eventsOpt    map[int64]gitlab.ListProjectVisibleEventsOptions
	pipelinesOpt map[int64]gitlab.ListProjectPipelinesOptions
}

func (f *fakeReader) ListProjects(opt *gitlab.ListProjectsOptions, _ ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Project, error] {
	f.projectsOpt = *opt

	return func(yield func(*gitlab.Project, error) bool) {
		if f.projectsErr != nil {
			yield(nil, f.projectsErr)

			return
		}

		for _, project := range f.projects {
			if !yield(project, nil) {
				return
			}
		}
	}
}

func (f *fakeReader) ListProjectVisibleEvents(pid any, opt *gitlab.ListProjectVisibleEventsOptions, _ ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.ProjectEvent, error] {
	projectID, _ := pid.(int64)

	if f.eventsOpt == nil {
		f.eventsOpt = map[int64]gitlab.ListProjectVisibleEventsOptions{}
	}

	f.eventsOpt[projectID] = *opt

	return func(yield func(*gitlab.ProjectEvent, error) bool) {
		if err := f.eventsErr[projectID]; err != nil {
			yield(nil, err)

			return
		}

		for _, event := range f.events[projectID] {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (f *fakeReader) ListProjectPipelines(pid any, opt *gitlab.ListProjectPipelinesOptions, _ ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.PipelineInfo, error] {
	projectID, _ := pid.(int64)

	if f.pipelinesOpt == nil {
		f.pipelinesOpt = map[int64]gitlab.ListProjectPipelinesOptions{}
	}

	f.pipelinesOpt[projectID] = *opt

	return func(yield func(*gitlab.PipelineInfo, error) bool) {
		if err := f.pipelinesErr[projectID]; err != nil {
			yield(nil, err)

			return
		}

		for _, info := range f.pipelines[projectID] {
			if !yield(info, nil) {
				return
			}
		}
	}
}

func (f *fakeReader) GetPipeline(_ any, pipeline int64, _ ...gitlab.RequestOptionFunc) (*gitlab.Pipeline, error) {
	found, ok := f.details[pipeline]
	if !ok {
		return nil, gitlab.ErrNotFound
	}

	return found, nil
}

// recorder collects everything a pass hands to the achievement engine.
type recorder struct {
	events []activity.Event
	err    error
}

func (r *recorder) Process(_ context.Context, event activity.Event) error {
	if r.err != nil {
		return r.err
	}

	r.events = append(r.events, event)

	return nil
}

func (r *recorder) keys() []string {
	keys := make([]string, 0, len(r.events))
	for _, event := range r.events {
		keys = append(keys, event.DedupKey)
	}

	return keys
}

func statusErr(code int) error {
	return &gitlab.ErrorResponse{StatusCode: code, Message: http.StatusText(code)}
}

// oneProjectInstance is a minimal instance: one group project with a push
// and a successful pipeline in the window.
func oneProjectInstance() *fakeReader {
	createdAt := startedAt.Add(-time.Hour)

	return &fakeReader{
		projects: []*gitlab.Project{groupProject(1)},
		events: map[int64][]*gitlab.ProjectEvent{
			1: {{
				ID: 100, AuthorID: 10, AuthorUsername: "alice", ProjectID: 1,
				ActionName: "pushed to", CreatedAt: createdAt.Format(time.RFC3339),
				PushData: gitlab.ProjectEventPushData{
					Action: "pushed", RefType: "branch", Ref: "main",
					CommitTo: "abc", CommitCount: 3,
				},
			}},
		},
		pipelines: map[int64][]*gitlab.PipelineInfo{
			1: {{ID: 500}},
		},
		details: map[int64]*gitlab.Pipeline{
			500: {
				ID: 500, ProjectID: 1, Status: "success", CreatedAt: &createdAt,
				User: &gitlab.BasicUser{ID: 10, Username: "alice"},
			},
		},
	}
}

func run(t *testing.T, read activityReader, conn *gorm.DB, processor activity.Processor) (*Report, error) {
	t.Helper()

	return Run(t.Context(), read, conn, processor, Options{
		Lookback: testLookback,
		Now:      func() time.Time { return startedAt },
	})
}

func TestRun_ReReadsRecentActivity(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()
	processor := &recorder{}

	report, err := run(t, read, conn, processor)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Projects != 1 || report.Events != 1 || report.Pipelines != 1 {
		t.Errorf("expected one project, event, and pipeline, got %+v", report)
	}

	// The keys are the webhook path's, which is the whole point: a pass
	// over a window live ingestion covered correctly must be a no-op.
	want := []string{
		"push:1:refs/heads/main:abc:push",
		"push:1:refs/heads/main:abc:commit",
		"pipeline:500:pipeline_run",
		"pipeline:500:pipeline_succeeded",
	}

	got := processor.keys()
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("activity %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestRun_AsksGitLabOnlyForRecentlyActiveProjects(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()

	_, err := run(t, read, conn, &recorder{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Without this filter every pass would cost a request per project on
	// the instance, however quiet it was, which is what makes a daily
	// sweep affordable at all.
	if read.projectsOpt.LastActivityAfter == nil {
		t.Fatal("expected the project listing to be narrowed to recently active projects")
	}

	want := startedAt.Add(-testLookback)
	if !read.projectsOpt.LastActivityAfter.Equal(want) {
		t.Errorf("expected projects active since %s, got %s", want, read.projectsOpt.LastActivityAfter)
	}
}

// GitLab's event date filter is day-granular and excludes the date given,
// so a window opening partway through a day has to ask for the day before
// or lose that day's earlier half.
func TestRun_AsksForEventsFromADayBeforeTheWindow(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()

	_, err := run(t, read, conn, &recorder{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	after := read.eventsOpt[1].After
	if after == nil {
		t.Fatal("expected an after filter on the event listing")
	}

	want := gitlab.ISOTime(startedAt.Add(-testLookback).AddDate(0, 0, -1))
	if time.Time(*after) != time.Time(want) {
		t.Errorf("expected events after %s, got %s", time.Time(want), time.Time(*after))
	}
}

// A pipeline that started before the window and finished inside it is
// exactly the case a lost delivery costs, so pipelines are selected by
// when they were last updated rather than when they were created.
func TestRun_SelectsPipelinesByUpdateTime(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()

	_, err := run(t, read, conn, &recorder{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	opt := read.pipelinesOpt[1]
	if opt.UpdatedAfter == nil {
		t.Fatal("expected pipelines to be filtered on their update time")
	}

	if opt.CreatedAfter != nil {
		t.Error("expected no creation filter, it would miss a pipeline that finished inside the window")
	}

	want := startedAt.Add(-testLookback)
	if !opt.UpdatedAfter.Equal(want) {
		t.Errorf("expected pipelines updated since %s, got %s", want, opt.UpdatedAfter)
	}
}

func TestRun_SkipsProjectsOutsideAGroup(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()
	read.projects = append(read.projects, userProject(2))

	report, err := run(t, read, conn, &recorder{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Projects != 1 || report.ProjectsSkipped != 1 {
		t.Errorf("expected the personal-namespace project to be skipped, got %+v", report)
	}
}

func TestRun_SkipsProjectsItCannotRead(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()
	read.projects = append(read.projects, groupProject(2))
	read.eventsErr = map[int64]error{2: statusErr(http.StatusForbidden)}

	report, err := run(t, read, conn, &recorder{})
	if err != nil {
		t.Fatalf("expected one unreadable project not to fail the pass, got: %v", err)
	}

	if report.Projects != 1 || report.ProjectsSkipped != 1 {
		t.Errorf("expected the unreadable project to be skipped, got %+v", report)
	}
}

func TestRun_StopsOnAnInstanceWideFailure(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()
	read.eventsErr = map[int64]error{1: statusErr(http.StatusInternalServerError)}

	_, err := run(t, read, conn, &recorder{})
	if err == nil {
		t.Fatal("expected a failing instance to fail the pass")
	}
}

// The watermark is what makes a failed pass recoverable: leaving it alone
// means the next pass covers the same window rather than stepping over the
// half that was never read.
func TestRun_LeavesTheWatermarkAloneOnFailure(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()
	read.projectsErr = errors.New("gitlab is down")

	_, err := run(t, read, conn, &recorder{})
	if err == nil {
		t.Fatal("expected the pass to fail")
	}

	if _, found, stateErr := CompletedAt(conn); stateErr != nil || found {
		t.Errorf("expected no watermark after a failed pass, found=%v err=%v", found, stateErr)
	}
}

// It records when the pass began, not when it ended: activity that happens
// while a pass runs may have been walked past before it existed, so a
// watermark at the finish line would step over it.
func TestRun_RecordsTheWatermarkAtTheStartOfThePass(t *testing.T) {
	conn := testConn(t)

	_, err := run(t, oneProjectInstance(), conn, &recorder{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	completedAt, found, err := CompletedAt(conn)
	if err != nil || !found {
		t.Fatalf("expected a watermark, found=%v err=%v", found, err)
	}

	if !completedAt.Equal(startedAt) {
		t.Errorf("expected the watermark at %s, got %s", startedAt, completedAt)
	}
}

func TestRun_ReportsNoGapOnARoutinePass(t *testing.T) {
	conn := testConn(t)

	if err := markCompleted(conn, startedAt.Add(-24*time.Hour)); err != nil {
		t.Fatalf("failed to seed the watermark: %v", err)
	}

	report, err := run(t, oneProjectInstance(), conn, &recorder{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Gap != 0 {
		t.Errorf("expected no gap when the look-back already covers the last pass, got %s", report.Gap)
	}

	if !report.Since.Equal(startedAt.Add(-testLookback)) {
		t.Errorf("expected the configured look-back, got a window from %s", report.Since)
	}
}

// An app that was down for a week comes back and re-reads the week, rather
// than covering two days and abandoning the other five to a hole nothing
// will revisit.
func TestRun_WidensTheWindowToCoverMissedPasses(t *testing.T) {
	conn := testConn(t)
	lastPass := startedAt.Add(-7 * 24 * time.Hour)

	if err := markCompleted(conn, lastPass); err != nil {
		t.Fatalf("failed to seed the watermark: %v", err)
	}

	report, err := run(t, oneProjectInstance(), conn, &recorder{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	want := lastPass.Add(-watermarkSlack)
	if !report.Since.Equal(want) {
		t.Errorf("expected the window to reach back to %s, got %s", want, report.Since)
	}

	if report.Gap <= 0 {
		t.Error("expected the missed time to be reported, so it is visible rather than silent")
	}
}

func TestRun_PropagatesProcessorFailures(t *testing.T) {
	conn := testConn(t)

	_, err := run(t, oneProjectInstance(), conn, &recorder{err: errors.New("database is down")})
	if err == nil {
		t.Fatal("expected a failing processor to fail the pass")
	}

	if _, found, _ := CompletedAt(conn); found {
		t.Error("expected no watermark after a pass whose activity was never counted")
	}
}

func TestCompletedAt_RejectsAnUnreadableWatermark(t *testing.T) {
	conn := testConn(t)

	if err := conn.Create(&appdb.SyncState{Key: completedStateKey, Value: "yesterday"}).Error; err != nil {
		t.Fatalf("failed to seed the watermark: %v", err)
	}

	if _, _, err := CompletedAt(conn); err == nil {
		t.Error("expected an unparseable watermark to be reported rather than treated as absent")
	}
}
