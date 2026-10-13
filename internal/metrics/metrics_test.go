package metrics

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMiddleware_LabelsByMatchedPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(resp http.ResponseWriter, _ *http.Request) {
		resp.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /webhooks/gitlab", func(resp http.ResponseWriter, _ *http.Request) {
		resp.WriteHeader(http.StatusUnauthorized)
	})

	handler := Middleware(mux)

	before := testutil.ToFloat64(httpRequests.WithLabelValues("/webhooks/gitlab", http.MethodPost, "401"))

	for range 2 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", nil))
	}

	after := testutil.ToFloat64(httpRequests.WithLabelValues("/webhooks/gitlab", http.MethodPost, "401"))
	if after-before != 2 {
		t.Fatalf("expected 2 requests counted on the webhook route, got %v", after-before)
	}

	unmatchedBefore := testutil.ToFloat64(httpRequests.WithLabelValues(unmatchedRoute, http.MethodGet, "404"))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	unmatchedAfter := testutil.ToFloat64(httpRequests.WithLabelValues(unmatchedRoute, http.MethodGet, "404"))
	if unmatchedAfter-unmatchedBefore != 1 {
		t.Fatalf("expected an unrouted path to be counted as %q, got delta %v", unmatchedRoute, unmatchedAfter-unmatchedBefore)
	}
}

func TestMiddleware_DefaultStatusIsOK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /implicit", func(resp http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(resp, "body without an explicit status")
	})

	before := testutil.ToFloat64(httpRequests.WithLabelValues("/implicit", http.MethodGet, "200"))

	Middleware(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/implicit", nil))

	if testutil.ToFloat64(httpRequests.WithLabelValues("/implicit", http.MethodGet, "200"))-before != 1 {
		t.Fatal("expected a handler that never called WriteHeader to be counted as 200")
	}
}

func TestMethodLabel_BoundsUnknownMethods(t *testing.T) {
	if got := methodLabel("PROPFIND"); got != "other" {
		t.Fatalf("methodLabel(PROPFIND) = %q, want other", got)
	}

	if got := methodLabel(http.MethodPost); got != http.MethodPost {
		t.Fatalf("methodLabel(POST) = %q, want POST", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTransport_CountsStatusesAndTransportErrors(t *testing.T) {
	statuses := []int{http.StatusOK, http.StatusTooManyRequests}
	calls := 0

	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls > len(statuses) {
			return nil, errors.New("connection refused")
		}

		return &http.Response{StatusCode: statuses[calls-1], Body: http.NoBody}, nil
	})

	rt := Transport("test-client", base)

	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "https://gitlab.example.com/api/v4/user", nil)

		resp, err := rt.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}

	for _, code := range []string{"200", "429", "error"} {
		if got := testutil.ToFloat64(gitlabRequests.WithLabelValues("test-client", http.MethodGet, code)); got != 1 {
			t.Errorf("expected one request counted with code %s, got %v", code, got)
		}
	}
}

func TestJob_RecordsResultAndLastSuccess(t *testing.T) {
	err := Job("test_job", func() error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	failure := errors.New("sweep failed")

	err = Job("test_job", func() error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("expected the task's error to be returned, got %v", err)
	}

	if got := testutil.ToFloat64(jobRuns.WithLabelValues("test_job", jobSuccess)); got != 1 {
		t.Errorf("expected one success, got %v", got)
	}

	if got := testutil.ToFloat64(jobRuns.WithLabelValues("test_job", jobFailure)); got != 1 {
		t.Errorf("expected one failure, got %v", got)
	}

	if testutil.ToFloat64(jobLastSuccess.WithLabelValues("test_job")) == 0 {
		t.Error("expected the last success timestamp to be set")
	}
}

func TestTrackQueue_ReplacesTheSource(t *testing.T) {
	TrackQueue(func() int { return 3 }, 10)
	TrackQueue(func() int { return 7 }, 20)

	if got := testutil.ToFloat64(queuePending); got != 7 {
		t.Errorf("expected pending to read the latest queue, got %v", got)
	}

	if got := testutil.ToFloat64(queueCapacity); got != 20 {
		t.Errorf("expected capacity to read the latest queue, got %v", got)
	}
}

func TestHandler_ServesTheTextFormat(t *testing.T) {
	SetBuildInfo("v-test")
	SetReady(true)

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	for _, want := range []string{
		`gitlab_achievements_build_info{goversion=`,
		`version="v-test"`,
		"gitlab_achievements_ready 1",
		"go_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected the metrics page to contain %q", want)
		}
	}
}
