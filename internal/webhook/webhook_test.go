package webhook

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
)

// fakeDispatcher records what the receiver accepted, and can be made to
// refuse.
type fakeDispatcher struct {
	mu     sync.Mutex
	events []activity.Event
	err    error
	calls  int
}

func (f *fakeDispatcher) Enqueue(_ context.Context, events []activity.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.err != nil {
		return f.err
	}

	f.events = append(f.events, events...)

	return nil
}

func (f *fakeDispatcher) accepted() []activity.Event {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.events
}

const pushPayload = `{
  "object_kind": "push",
  "user_id": 10,
  "user_username": "alice",
  "project_id": 1,
  "ref": "refs/heads/main",
  "before": "aaaa",
  "after": "bbbb",
  "total_commits_count": 2
}`

func deliver(t *testing.T, receiver *Receiver, token, eventType, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	if token != "" {
		req.Header.Set("X-Gitlab-Token", token)
	}

	if eventType != "" {
		req.Header.Set("X-Gitlab-Event", eventType)
	}

	resp := httptest.NewRecorder()
	receiver.ServeHTTP(resp, req)

	return resp
}

func TestReceiver_AcceptsAValidDelivery(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "s3cr3t", "Push Hook", pushPayload)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.Code)
	}

	if len(queue.accepted()) != 2 {
		t.Errorf("expected the push and its commits to be queued, got %d", len(queue.accepted()))
	}
}

func TestReceiver_RejectsAWrongToken(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "wrong", "Push Hook", pushPayload)

	if resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for a delivery that didn't come from our hook, got %d", resp.Code)
	}

	if queue.calls != 0 {
		t.Error("expected an unauthenticated delivery not to reach the queue")
	}
}

func TestReceiver_RejectsAMissingToken(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "", "Push Hook", pushPayload)

	if resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when no token is presented, got %d", resp.Code)
	}
}

func TestReceiver_RejectsAnEmptyConfiguredSecretPresentedAsAbsent(t *testing.T) {
	// A blank secret would otherwise make every unauthenticated delivery
	// valid. Configuration requires webhook-secret, so this can only happen
	// by mistake; the token still has to match, and a blank one matching a
	// blank secret is the one case worth pinning down.
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "", "Push Hook", pushPayload)
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.Code)
	}
}

func TestReceiver_IgnoresEventTypesItTracksNothingFor(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	// A group hook delivers subgroup events whether or not this app wants
	// them. Answering anything but 200 would eventually get the hook
	// disabled over events that were never interesting.
	resp := deliver(t, receiver, "s3cr3t", "Subgroup Hook", `{"object_kind":"subgroup"}`)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200 for an event type this app ignores, got %d", resp.Code)
	}

	if queue.calls != 0 {
		t.Error("expected nothing to be queued for an ignored event type")
	}
}

func TestReceiver_AcknowledgesAnUnparseablePayload(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	// Redelivering it would fail identically, so failing the delivery would
	// only cost the hook its registration.
	resp := deliver(t, receiver, "s3cr3t", "Push Hook", `{"object_kind": nonsense`)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200 for a payload no redelivery would fix, got %d", resp.Code)
	}
}

func TestReceiver_AcknowledgesAPayloadCarryingNoTrackedActivity(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "s3cr3t", "Merge Request Hook",
		`{"object_kind":"merge_request","user":{"id":10},"object_attributes":{"id":55,"action":"update"}}`)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.Code)
	}

	if queue.calls != 0 {
		t.Error("expected an action this app tracks nothing for not to be queued")
	}
}

func TestReceiver_ReportsBackpressureAsRetryable(t *testing.T) {
	queue := &fakeDispatcher{err: ErrQueueFull}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "s3cr3t", "Push Hook", pushPayload)

	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when the app can't take responsibility for a delivery, got %d", resp.Code)
	}
}

func TestReceiver_ReportsShutdownAsRetryable(t *testing.T) {
	queue := &fakeDispatcher{err: ErrShuttingDown}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "s3cr3t", "Push Hook", pushPayload)

	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 during shutdown, got %d", resp.Code)
	}
}

func TestReceiver_StopsReadingAnOversizedBody(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	oversized := `{"object_kind":"push","user_id":10,"ref":"` + strings.Repeat("x", maxPayloadBytes+1) + `"}`

	resp := deliver(t, receiver, "s3cr3t", "Push Hook", oversized)

	if resp.Code != http.StatusOK {
		t.Errorf("expected the delivery to be acknowledged rather than failed, got %d", resp.Code)
	}

	if queue.calls != 0 {
		t.Error("expected an oversized body not to be processed")
	}
}

func TestReceiver_ConfidentialCommentsAreIngestedToo(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "s3cr3t", "Confidential Note Hook", `{
      "object_kind": "note",
      "user": {"id": 10, "username": "alice"},
      "project_id": 1,
      "object_attributes": {"id": 7, "author_id": 10, "noteable_type": "Issue", "note": "hi"},
      "issue": {"id": 3}
    }`)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.Code)
	}

	// Work on confidential issues is still work; the app keeps only the
	// note's identity and author, never its content.
	if len(queue.accepted()) != 1 {
		t.Errorf("expected a confidential comment to count, got %d activities", len(queue.accepted()))
	}
}

func TestReceiver_DoesNotLeakWhetherATokenWasClose(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	for _, token := range []string{"s3cr3", "s3cr3u", "s3cr3t-longer"} {
		resp := deliver(t, receiver, token, "Push Hook", pushPayload)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("token %q: expected 401, got %d", token, resp.Code)
		}
	}
}

func TestReceiver_ParseErrorsAreNotFatalToTheProcess(t *testing.T) {
	queue := &fakeDispatcher{err: errors.New("boom")}
	receiver := NewReceiver("s3cr3t", queue)

	resp := deliver(t, receiver, "s3cr3t", "Push Hook", pushPayload)
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("expected an enqueue failure to surface as 503, got %d", resp.Code)
	}
}

// fakeDiscussions serves one merge request's discussions, or an error.
type fakeDiscussions struct {
	discussions []*gitlab.Discussion
	err         error
	calls       int
}

func (f *fakeDiscussions) ListMergeRequestDiscussions(_ any, _ int64, _ *gitlab.ListMergeRequestDiscussionsOptions, _ ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Discussion, error] {
	f.calls++

	return func(yield func(*gitlab.Discussion, error) bool) {
		if f.err != nil {
			yield(nil, f.err)

			return
		}

		for _, discussion := range f.discussions {
			if !yield(discussion, nil) {
				return
			}
		}
	}
}

const bareMergeRequestUpdate = `{
  "object_kind": "merge_request",
  "user": {"id": 10, "username": "alice"},
  "project": {"id": 1},
  "object_attributes": {"id": 5, "iid": 3, "target_project_id": 1, "action": "update"},
  "changes": {}
}`

func resolvedByAlice() []*gitlab.Discussion {
	when := time.Date(2026, time.March, 2, 11, 0, 0, 0, time.UTC)

	return []*gitlab.Discussion{{ID: "abc123", Notes: []*gitlab.Note{{
		Resolvable: true, Resolved: true, ResolvedAt: &when,
		ResolvedBy: gitlab.NoteResolvedBy{ID: 10, Username: "alice"},
	}}}}
}

func TestReceiver_ABareMergeRequestUpdateIsCheckedForResolvedDiscussions(t *testing.T) {
	queue := &fakeDispatcher{}
	source := &fakeDiscussions{discussions: resolvedByAlice()}
	receiver := NewReceiver("s3cr3t", queue, WithDiscussions(source))

	resp := deliver(t, receiver, "s3cr3t", "Merge Request Hook", bareMergeRequestUpdate)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}

	events := queue.accepted()
	if len(events) != 1 || events[0].Kind != activity.KindDiscussionResolved || events[0].ActorID != 10 {
		t.Errorf("expected alice's resolution to be queued, got %+v", events)
	}
}

func TestReceiver_AnUpdateThatChangedSomethingCostsNoAPICall(t *testing.T) {
	queue := &fakeDispatcher{}
	source := &fakeDiscussions{discussions: resolvedByAlice()}
	receiver := NewReceiver("s3cr3t", queue, WithDiscussions(source))

	body := strings.Replace(bareMergeRequestUpdate, `"changes": {}`, `"changes": {"title": {"previous": "a", "current": "b"}}`, 1)
	deliver(t, receiver, "s3cr3t", "Merge Request Hook", body)

	if source.calls != 0 {
		t.Errorf("expected no discussions read for an update that changed the title, got %d", source.calls)
	}
}

func TestReceiver_AFailedDiscussionReadDropsNothingElseAndStillAnswers200(t *testing.T) {
	queue := &fakeDispatcher{}
	source := &fakeDiscussions{err: errors.New("boom")}
	receiver := NewReceiver("s3cr3t", queue, WithDiscussions(source))

	resp := deliver(t, receiver, "s3cr3t", "Merge Request Hook", bareMergeRequestUpdate)

	if resp.Code != http.StatusOK || len(queue.accepted()) != 0 {
		t.Errorf("expected a quiet 200 with nothing queued, got %d and %+v", resp.Code, queue.accepted())
	}
}

func TestReceiver_WithoutADiscussionSourceAnUpdateIsIgnored(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue)

	deliver(t, receiver, "s3cr3t", "Merge Request Hook", bareMergeRequestUpdate)

	if len(queue.accepted()) != 0 {
		t.Errorf("expected nothing without a source, got %+v", queue.accepted())
	}
}

// fakeReleases serves one release, or an error.
type fakeReleases struct {
	release *gitlab.Release
	err     error
	asked   []string
}

func (f *fakeReleases) GetRelease(pid any, tagName string, _ ...gitlab.RequestOptionFunc) (*gitlab.Release, error) {
	f.asked = append(f.asked, fmt.Sprintf("%v/%s", pid, tagName))

	return f.release, f.err
}

const releaseCreatedHook = `{
  "object_kind": "release",
  "action": "create",
  "tag": "v1.0.0",
  "project": {"id": 1}
}`

func aliceRelease() *gitlab.Release {
	createdAt := time.Date(2026, time.March, 2, 11, 0, 0, 0, time.UTC)

	return &gitlab.Release{
		TagName: "v1.0.0", CreatedAt: &createdAt,
		Author: gitlab.BasicUser{ID: 10, Username: "alice"},
	}
}

func TestReceiver_AReleaseIsCreditedToTheAuthorTheAPINames(t *testing.T) {
	queue := &fakeDispatcher{}
	source := &fakeReleases{release: aliceRelease()}
	receiver := NewReceiver("s3cr3t", queue, WithReleases(source))

	resp := deliver(t, receiver, "s3cr3t", "Release Hook", releaseCreatedHook)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}

	if len(source.asked) != 1 || source.asked[0] != "1/v1.0.0" {
		t.Errorf("expected the release looked up by project and tag, got %v", source.asked)
	}

	events := queue.accepted()
	if len(events) != 1 || events[0].Kind != activity.KindReleasePublished || events[0].ActorID != 10 {
		t.Fatalf("expected alice's release to be queued, got %+v", events)
	}

	if events[0].DedupKey != "release:1:v1.0.0" {
		t.Errorf("expected the release keyed on project and tag, got %q", events[0].DedupKey)
	}
}

func TestReceiver_OnlyACreatedReleaseCostsALookup(t *testing.T) {
	queue := &fakeDispatcher{}
	source := &fakeReleases{release: aliceRelease()}
	receiver := NewReceiver("s3cr3t", queue, WithReleases(source))

	for _, action := range []string{"update", "delete"} {
		deliver(t, receiver, "s3cr3t", "Release Hook", strings.Replace(releaseCreatedHook, `"create"`, `"`+action+`"`, 1))
	}

	if len(source.asked) != 0 || len(queue.accepted()) != 0 {
		t.Errorf("expected updates and deletions to be ignored, got lookups %v and %+v", source.asked, queue.accepted())
	}
}

func TestReceiver_AFailedReleaseReadStillAnswers200(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue, WithReleases(&fakeReleases{err: errors.New("boom")}))

	resp := deliver(t, receiver, "s3cr3t", "Release Hook", releaseCreatedHook)

	if resp.Code != http.StatusOK || len(queue.accepted()) != 0 {
		t.Errorf("expected a quiet 200 with nothing queued, got %d and %+v", resp.Code, queue.accepted())
	}
}

// fakePipelineHistory reports one outcome for whatever it is asked.
type fakePipelineHistory struct {
	previous gitlab.BuildStateValue
	err      error
	asked    []string
}

func (f *fakePipelineHistory) LastPipelineOutcome(pid any, ref string, beforeID int64, _ ...gitlab.RequestOptionFunc) (gitlab.BuildStateValue, error) {
	f.asked = append(f.asked, fmt.Sprintf("%v/%s/%d", pid, ref, beforeID))

	return f.previous, f.err
}

const pipelinePassedHook = `{
  "object_kind": "pipeline",
  "user": {"id": 10, "username": "alice"},
  "project": {"id": 1},
  "object_attributes": {"id": 600, "ref": "main", "status": "success", "created_at": "2026-03-04 11:00:00 UTC"}
}`

func fixesIn(events []activity.Event) []activity.Event {
	var fixes []activity.Event

	for _, event := range events {
		if event.Kind == activity.KindPipelineFixed {
			fixes = append(fixes, event)
		}
	}

	return fixes
}

func TestReceiver_APassAfterAFailureFixesTheBuild(t *testing.T) {
	queue := &fakeDispatcher{}
	history := &fakePipelineHistory{previous: gitlab.Failed}
	receiver := NewReceiver("s3cr3t", queue, WithPipelineHistory(history))

	deliver(t, receiver, "s3cr3t", "Pipeline Hook", pipelinePassedHook)

	if len(history.asked) != 1 || history.asked[0] != "1/main/600" {
		t.Errorf("expected the ref's history looked up before pipeline 600, got %v", history.asked)
	}

	fixes := fixesIn(queue.accepted())
	if len(fixes) != 1 || fixes[0].DedupKey != "pipeline:600:pipeline_fixed" || fixes[0].ActorID != 10 {
		t.Errorf("expected alice's fix to be queued, got %+v", fixes)
	}
}

func TestReceiver_APassAfterAPassFixesNothing(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue, WithPipelineHistory(&fakePipelineHistory{previous: gitlab.Success}))

	deliver(t, receiver, "s3cr3t", "Pipeline Hook", pipelinePassedHook)

	if fixes := fixesIn(queue.accepted()); len(fixes) != 0 {
		t.Errorf("expected no fix, got %+v", fixes)
	}

	if len(queue.accepted()) == 0 {
		t.Error("expected the pipeline itself still queued")
	}
}

func TestReceiver_OnlyAPassingPipelineCostsALookup(t *testing.T) {
	queue := &fakeDispatcher{}
	history := &fakePipelineHistory{previous: gitlab.Failed}
	receiver := NewReceiver("s3cr3t", queue, WithPipelineHistory(history))

	for _, status := range []string{"running", "failed", "canceled"} {
		deliver(t, receiver, "s3cr3t", "Pipeline Hook", strings.Replace(pipelinePassedHook, `"success"`, `"`+status+`"`, 1))
	}

	if len(history.asked) != 0 {
		t.Errorf("expected no lookups, got %v", history.asked)
	}
}

func TestReceiver_AFailedPipelineLookupKeepsThePipeline(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue, WithPipelineHistory(&fakePipelineHistory{err: errors.New("boom")}))

	resp := deliver(t, receiver, "s3cr3t", "Pipeline Hook", pipelinePassedHook)

	if resp.Code != http.StatusOK || len(queue.accepted()) == 0 || len(fixesIn(queue.accepted())) != 0 {
		t.Errorf("expected a 200 with the pipeline but no fix, got %d and %+v", resp.Code, queue.accepted())
	}
}
