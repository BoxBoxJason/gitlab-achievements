package gitlabclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
)

// pipelinesServer answers every pipeline listing with body, recording the
// query it was asked.
func pipelinesServer(t *testing.T, body string, query *string) *ReadClient {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*query = r.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	client, err := NewReadClient(server.URL, "token")
	if err != nil {
		t.Fatalf("failed to build client: %v", err)
	}

	return client
}

func TestLastPipelineOutcome_SkipsNewerAndUnfinishedPipelines(t *testing.T) {
	var query string

	// Newest first, as asked for: 12 is the pipeline being judged and 11 is
	// newer than nothing it matters to; 10 was canceled and says nothing.
	client := pipelinesServer(t, `[
		{"id": 12, "ref": "main", "status": "success"},
		{"id": 10, "ref": "main", "status": "canceled"},
		{"id": 9, "ref": "main", "status": "failed"},
		{"id": 8, "ref": "main", "status": "success"}
	]`, &query)

	got, err := client.LastPipelineOutcome(1, "main", 12)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got != gitlab.Failed {
		t.Errorf("expected pipeline 9's failure, got %q", got)
	}

	for _, want := range []string{"ref=main", "order_by=id", "sort=desc"} {
		if !strings.Contains(query, want) {
			t.Errorf("expected the listing to ask for %s, got %q", want, query)
		}
	}
}

func TestLastPipelineOutcome_ReportsNothingWithoutAFinishedPredecessor(t *testing.T) {
	var query string

	client := pipelinesServer(t, `[{"id": 12, "ref": "main", "status": "success"}, {"id": 11, "ref": "main", "status": "skipped"}]`, &query)

	got, err := client.LastPipelineOutcome(1, "main", 12)
	if err != nil || got != "" {
		t.Errorf("expected no outcome, got %q (%v)", got, err)
	}
}
