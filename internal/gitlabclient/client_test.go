package gitlabclient

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestWriteClient_DoesNotReplayMutationsOn5xx guards against duplicate
// awards: achievementsAward is not idempotent, and a 5xx can follow a
// mutation GitLab already committed, so replaying it awards again.
func TestWriteClient_DoesNotReplayMutationsOn5xx(t *testing.T) {
	var posts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}

		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	client, err := NewWriteClient(server.URL, "dummy-token")
	if err != nil {
		t.Fatalf("failed to build the write client: %v", err)
	}

	_, err = client.AwardAchievement(1, 2, nil)
	if err == nil {
		t.Fatal("expected the 502 to surface as an error, got nil")
	}

	if got := posts.Load(); got != 1 {
		t.Errorf("expected exactly one achievementsAward request, got %d", got)
	}
}
