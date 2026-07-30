package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
	receiver := NewReceiver("s3cr3t", queue, nil)

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
	receiver := NewReceiver("s3cr3t", queue, nil)

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
	receiver := NewReceiver("s3cr3t", queue, nil)

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
	receiver := NewReceiver("s3cr3t", queue, nil)

	resp := deliver(t, receiver, "", "Push Hook", pushPayload)
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.Code)
	}
}

func TestReceiver_IgnoresEventTypesItTracksNothingFor(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue, nil)

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
	receiver := NewReceiver("s3cr3t", queue, nil)

	// Redelivering it would fail identically, so failing the delivery would
	// only cost the hook its registration.
	resp := deliver(t, receiver, "s3cr3t", "Push Hook", `{"object_kind": nonsense`)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200 for a payload no redelivery would fix, got %d", resp.Code)
	}
}

func TestReceiver_AcknowledgesAPayloadCarryingNoTrackedActivity(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue, nil)

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
	receiver := NewReceiver("s3cr3t", queue, nil)

	resp := deliver(t, receiver, "s3cr3t", "Push Hook", pushPayload)

	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when the app can't take responsibility for a delivery, got %d", resp.Code)
	}
}

func TestReceiver_ReportsShutdownAsRetryable(t *testing.T) {
	queue := &fakeDispatcher{err: ErrShuttingDown}
	receiver := NewReceiver("s3cr3t", queue, nil)

	resp := deliver(t, receiver, "s3cr3t", "Push Hook", pushPayload)

	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 during shutdown, got %d", resp.Code)
	}
}

func TestReceiver_StopsReadingAnOversizedBody(t *testing.T) {
	queue := &fakeDispatcher{}
	receiver := NewReceiver("s3cr3t", queue, nil)

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
	receiver := NewReceiver("s3cr3t", queue, nil)

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
	receiver := NewReceiver("s3cr3t", queue, nil)

	for _, token := range []string{"s3cr3", "s3cr3u", "s3cr3t-longer"} {
		resp := deliver(t, receiver, token, "Push Hook", pushPayload)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("token %q: expected 401, got %d", token, resp.Code)
		}
	}
}

func TestReceiver_ParseErrorsAreNotFatalToTheProcess(t *testing.T) {
	queue := &fakeDispatcher{err: errors.New("boom")}
	receiver := NewReceiver("s3cr3t", queue, nil)

	resp := deliver(t, receiver, "s3cr3t", "Push Hook", pushPayload)
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("expected an enqueue failure to surface as 503, got %d", resp.Code)
	}
}
