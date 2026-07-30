package gitlabclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWithRateLimit_PacesRequests(t *testing.T) {
	var served int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 1, "username": "bot"}`)) //nolint:errcheck // test server
	}))
	defer server.Close()

	const perSecond = 20.0

	client, err := NewReadClient(server.URL, "token", WithRateLimit(perSecond, 1))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	start := time.Now()

	for range 3 {
		if _, err := client.CurrentUser(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	elapsed := time.Since(start)

	if served != 3 {
		t.Fatalf("expected 3 requests to be served, got %d", served)
	}

	// A burst of one lets the first request through immediately; the other
	// two each wait out the interval.
	minimum := 2 * time.Second / time.Duration(perSecond)
	if elapsed < minimum {
		t.Errorf("expected the cap to hold requests back for at least %s, took %s", minimum, elapsed)
	}
}

func TestWithRateLimit_ClampsBurstToUsable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 1, "username": "bot"}`)) //nolint:errcheck // test server
	}))
	defer server.Close()

	// A limiter built with a zero burst admits nothing at all, so the
	// option has to clamp rather than deadlock the caller.
	client, err := NewReadClient(server.URL, "token", WithRateLimit(100, 0))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if _, err := client.CurrentUser(); err != nil {
		t.Fatalf("expected the request to be admitted, got: %v", err)
	}
}
