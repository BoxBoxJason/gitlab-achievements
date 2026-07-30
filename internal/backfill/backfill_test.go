package backfill

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
)

// fakeReader serves a canned instance to the walk and records what it was
// asked for, so tests can assert on the request shape (resume cursors,
// look-back filters) as well as on the activity produced.
type fakeReader struct {
	projects  []*gitlab.Project
	events    map[int64][]*gitlab.ProjectEvent
	pipelines map[int64][]*gitlab.PipelineInfo
	details   map[int64]*gitlab.Pipeline

	projectsErr  error
	eventsErr    map[int64]error
	pipelinesErr map[int64]error

	projectsOpt   gitlab.ListProjectsOptions
	eventsOpt     map[int64]gitlab.ListProjectVisibleEventsOptions
	pipelinesOpt  map[int64]gitlab.ListProjectPipelinesOptions
	detailFetches []int64
}

func (f *fakeReader) ListProjects(opt *gitlab.ListProjectsOptions, _ ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Project, error] {
	f.projectsOpt = *opt

	return func(yield func(*gitlab.Project, error) bool) {
		if f.projectsErr != nil {
			yield(nil, f.projectsErr)

			return
		}

		for _, project := range f.projects {
			if opt.IDAfter != nil && project.ID <= *opt.IDAfter {
				continue
			}

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
	f.detailFetches = append(f.detailFetches, pipeline)

	found, ok := f.details[pipeline]
	if !ok {
		return nil, gitlab.ErrNotFound
	}

	return found, nil
}

// recorder collects everything the walk hands to the achievement engine.
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

func (r *recorder) kinds() []activity.Kind {
	kinds := make([]activity.Kind, 0, len(r.events))
	for _, event := range r.events {
		kinds = append(kinds, event.Kind)
	}

	return kinds
}

func statusErr(code int) error {
	return &gitlab.ErrorResponse{StatusCode: code, Message: http.StatusText(code)}
}

// oneProjectInstance is a minimal instance: one project with one push and
// one successful pipeline.
func oneProjectInstance() *fakeReader {
	createdAt := time.Date(2024, time.May, 3, 10, 0, 0, 0, time.UTC)

	return &fakeReader{
		projects: []*gitlab.Project{{ID: 1}},
		events: map[int64][]*gitlab.ProjectEvent{
			1: {{
				ID: 100, AuthorID: 10, AuthorUsername: "alice", ProjectID: 1,
				ActionName: "pushed to", CreatedAt: "2024-05-03T10:00:00Z",
				PushData: gitlab.ProjectEventPushData{Action: "pushed", RefType: "branch", CommitCount: 3},
			}},
		},
		pipelines: map[int64][]*gitlab.PipelineInfo{1: {{ID: 500, ProjectID: 1}}},
		details: map[int64]*gitlab.Pipeline{
			500: {ID: 500, ProjectID: 1, Status: "success", CreatedAt: &createdAt, User: &gitlab.BasicUser{ID: 10, Username: "alice"}},
		},
	}
}

func TestRun_WalksEventsAndPipelinesThroughTheProcessor(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()
	processor := &recorder{}

	report, err := Run(t.Context(), read, conn, processor, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	want := []activity.Kind{activity.KindPush, activity.KindCommit, activity.KindPipelineRun, activity.KindPipelineSucceeded}
	got := processor.kinds()

	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("activity %d: expected %q, got %q", i, want[i], got[i])
		}
	}

	if report.Projects != 1 || report.Events != 1 || report.Pipelines != 1 {
		t.Errorf("expected 1 project, 1 event and 1 pipeline, got %+v", report)
	}

	if report.CompletedAt.IsZero() {
		t.Error("expected the report to carry a completion time")
	}
}

func TestRun_RecordsTheCompletionWatermark(t *testing.T) {
	conn := testConn(t)

	_, err := Run(t.Context(), oneProjectInstance(), conn, &recorder{}, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	completedAt, found, err := CompletedAt(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !found || completedAt.IsZero() {
		t.Error("expected a completion watermark so the app knows the cold start is over")
	}

	saved, err := loadProgress(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if saved != (progress{}) {
		t.Errorf("expected the in-flight cursor to be cleared on completion, got %+v", saved)
	}
}

func TestRun_DoesNothingOnceHistoryHasBeenWalked(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()

	_, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	second := &recorder{}

	report, err := Run(t.Context(), read, conn, second, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !report.AlreadyComplete {
		t.Errorf("expected the second run to report history as already walked, got %+v", report)
	}

	if len(second.events) != 0 {
		t.Errorf("expected a restart not to re-walk a finished instance, got %d activities", len(second.events))
	}
}

func TestRun_ForceReWalksAFinishedInstance(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()

	_, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	second := &recorder{}

	report, err := Run(t.Context(), read, conn, second, Options{Force: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.AlreadyComplete {
		t.Error("expected force to override the completion watermark")
	}

	if len(second.events) == 0 {
		t.Error("expected the forced run to walk the instance again")
	}
}

func TestRun_ResumesAfterTheLastFinishedProject(t *testing.T) {
	conn := testConn(t)

	read := oneProjectInstance()
	read.projects = []*gitlab.Project{{ID: 1}, {ID: 2}}
	read.events[2] = []*gitlab.ProjectEvent{{
		ID: 200, AuthorID: 11, ProjectID: 2, TargetType: "Issue", ActionName: "opened",
		CreatedAt: "2024-05-04T10:00:00Z",
	}}

	if err := saveProgress(conn, progress{LastProjectID: 1}); err != nil {
		t.Fatalf("failed to seed progress: %v", err)
	}

	processor := &recorder{}

	report, err := Run(t.Context(), read, conn, processor, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !report.Resumed {
		t.Error("expected the run to report itself as resumed")
	}

	if read.projectsOpt.IDAfter == nil || *read.projectsOpt.IDAfter != 1 {
		t.Errorf("expected projects to be requested after the last finished one, got %+v", read.projectsOpt.IDAfter)
	}

	if _, walked := read.eventsOpt[1]; walked {
		t.Error("expected the already-finished project not to be walked again")
	}

	if report.Projects != 1 {
		t.Errorf("expected only the remaining project to be walked, got %+v", report)
	}
}

func TestRun_ResumesMidProjectFromTheSavedPhaseAndCursors(t *testing.T) {
	conn := testConn(t)

	read := oneProjectInstance()
	read.pipelines[1] = []*gitlab.PipelineInfo{{ID: 499}, {ID: 500}}

	// Interrupted after the events phase, having already processed
	// pipeline 499.
	seeded := progress{Phase: phasePipelines, CurrentProjectID: 1, LastPipelineID: 499}
	if err := saveProgress(conn, seeded); err != nil {
		t.Fatalf("failed to seed progress: %v", err)
	}

	processor := &recorder{}

	_, err := Run(t.Context(), read, conn, processor, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if _, walked := read.eventsOpt[1]; walked {
		t.Error("expected the finished events phase not to be re-fetched")
	}

	if len(read.detailFetches) != 1 || read.detailFetches[0] != 500 {
		t.Errorf("expected only the unprocessed pipeline to be fetched, got %v", read.detailFetches)
	}
}

func TestRun_ResumedEventsWalkAsksForTheCursorDayOnwards(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()

	if err := saveProgress(conn, progress{Phase: phaseEvents, CurrentProjectID: 1, EventsCursor: "2024-05-03"}); err != nil {
		t.Fatalf("failed to seed progress: %v", err)
	}

	_, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	after := read.eventsOpt[1].After
	if after == nil {
		t.Fatal("expected the resumed walk to bound the events request")
	}

	// GitLab's "after" filter excludes the date given, so the cursor day
	// itself is re-read (and deduplicated) rather than skipped.
	want := time.Date(2024, time.May, 2, 0, 0, 0, 0, time.UTC)
	if !time.Time(*after).Equal(want) {
		t.Errorf("expected after=%s, got %s", want, time.Time(*after))
	}
}

func TestRun_AppliesTheLookBackWindowServerSide(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()
	since := time.Date(2024, time.January, 10, 0, 0, 0, 0, time.UTC)

	_, err := Run(t.Context(), read, conn, &recorder{}, Options{Since: since})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	after := read.eventsOpt[1].After
	if after == nil || !time.Time(*after).Equal(since.AddDate(0, 0, -1)) {
		t.Errorf("expected the events request to carry the look-back window, got %v", after)
	}

	createdAfter := read.pipelinesOpt[1].CreatedAfter
	if createdAfter == nil || !createdAfter.Equal(since) {
		t.Errorf("expected the pipelines request to carry the look-back window, got %v", createdAfter)
	}
}

func TestRun_WalksTheFullHistoryWhenUnbounded(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()

	_, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if read.eventsOpt[1].After != nil {
		t.Errorf("expected no lower bound on the events request, got %v", read.eventsOpt[1].After)
	}

	if read.pipelinesOpt[1].CreatedAfter != nil {
		t.Errorf("expected no lower bound on the pipelines request, got %v", read.pipelinesOpt[1].CreatedAfter)
	}
}

func TestRun_SkipsProjectsItCannotRead(t *testing.T) {
	conn := testConn(t)

	read := oneProjectInstance()
	read.projects = []*gitlab.Project{{ID: 1}, {ID: 2}}
	read.eventsErr = map[int64]error{1: statusErr(http.StatusForbidden)}
	read.events[2] = []*gitlab.ProjectEvent{{
		ID: 200, AuthorID: 11, ProjectID: 2, TargetType: "Issue", ActionName: "opened",
	}}

	processor := &recorder{}

	report, err := Run(t.Context(), read, conn, processor, Options{})
	if err != nil {
		t.Fatalf("expected an unreadable project not to fail the run, got: %v", err)
	}

	if report.ProjectsSkipped != 1 || report.Projects != 1 {
		t.Errorf("expected 1 skipped and 1 walked project, got %+v", report)
	}

	if len(processor.kinds()) == 0 {
		t.Error("expected the readable project to still be walked")
	}
}

func TestRun_SkipsProjectsDeletedMidWalk(t *testing.T) {
	conn := testConn(t)

	read := oneProjectInstance()
	read.eventsErr = map[int64]error{1: gitlab.ErrNotFound}

	report, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err != nil {
		t.Fatalf("expected a vanished project not to fail the run, got: %v", err)
	}

	if report.ProjectsSkipped != 1 {
		t.Errorf("expected the vanished project to be skipped, got %+v", report)
	}
}

func TestRun_StopsAndKeepsItsPlaceOnAnInstanceWideFailure(t *testing.T) {
	conn := testConn(t)

	read := oneProjectInstance()
	read.projects = []*gitlab.Project{{ID: 1}, {ID: 2}}
	read.eventsErr = map[int64]error{2: statusErr(http.StatusInternalServerError)}
	read.events[2] = []*gitlab.ProjectEvent{}

	_, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err == nil {
		t.Fatal("expected a server-side failure to stop the walk, got nil")
	}

	_, found, completionErr := CompletedAt(conn)
	if completionErr != nil {
		t.Fatalf("expected no error, got: %v", completionErr)
	}

	if found {
		t.Error("expected an interrupted walk not to claim completion")
	}

	saved, progressErr := loadProgress(conn)
	if progressErr != nil {
		t.Fatalf("expected no error, got: %v", progressErr)
	}

	if saved.LastProjectID != 1 {
		t.Errorf("expected the finished project to be recorded so a retry resumes after it, got %+v", saved)
	}
}

func TestRun_SkipsAPipelineDeletedBetweenListingAndFetching(t *testing.T) {
	conn := testConn(t)

	read := oneProjectInstance()
	read.pipelines[1] = []*gitlab.PipelineInfo{{ID: 404}}
	read.details = map[int64]*gitlab.Pipeline{}

	report, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err != nil {
		t.Fatalf("expected a vanished pipeline not to fail the run, got: %v", err)
	}

	if report.Pipelines != 0 || report.Projects != 1 {
		t.Errorf("expected the project to finish with no pipeline counted, got %+v", report)
	}
}

func TestRun_PropagatesProcessorFailures(t *testing.T) {
	conn := testConn(t)

	_, err := Run(t.Context(), oneProjectInstance(), conn, &recorder{err: errors.New("database is gone")}, Options{})
	if err == nil {
		t.Fatal("expected an engine failure to stop the walk, got nil")
	}
}

func TestRun_StopsWhenTheProjectListingFails(t *testing.T) {
	conn := testConn(t)

	read := oneProjectInstance()
	read.projectsErr = statusErr(http.StatusInternalServerError)

	_, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err == nil {
		t.Fatal("expected an unusable project listing to fail the run, got nil")
	}
}

func TestRun_RequestsKeysetPaginatedProjectsInIDOrder(t *testing.T) {
	conn := testConn(t)
	read := oneProjectInstance()

	_, err := Run(t.Context(), read, conn, &recorder{}, Options{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	opt := read.projectsOpt.ListOptions

	if opt.Pagination != keysetPagination || opt.OrderBy != "id" || opt.Sort != "asc" {
		t.Errorf("expected keyset pagination in ascending ID order, which is what makes the cursor resumable, got %+v", opt)
	}
}
