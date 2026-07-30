package httpserver_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/httpserver"
)

func okCheck(context.Context) error { return nil }

func TestHealthz_AlwaysOK(t *testing.T) {
	s := httpserver.New(func(context.Context) error {
		return errors.New("db down")
	}, func(context.Context) error {
		return errors.New("gitlab down")
	}, nil)

	rec := doRequest(t, s, http.MethodGet, "/healthz")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestReadyz_NotReadyBeforeSetReady(t *testing.T) {
	s := httpserver.New(okCheck, okCheck, nil)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 before SetReady(true), got %d", rec.Code)
	}
}

func TestReadyz_ReadyWhenChecksPass(t *testing.T) {
	s := httpserver.New(okCheck, okCheck, nil)
	s.SetReady(true)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestReadyz_UnreadyWhenDBCheckFails(t *testing.T) {
	s := httpserver.New(func(context.Context) error {
		return errors.New("db down")
	}, okCheck, nil)
	s.SetReady(true)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when the db check fails, got %d", rec.Code)
	}
}

func TestReadyz_UnreadyWhenGitLabCheckFails(t *testing.T) {
	s := httpserver.New(okCheck, func(context.Context) error {
		return errors.New("gitlab down")
	}, nil)
	s.SetReady(true)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when the gitlab check fails, got %d", rec.Code)
	}
}

func TestReadyz_CanFlipBackToUnready(t *testing.T) {
	s := httpserver.New(okCheck, okCheck, nil)
	s.SetReady(true)
	s.SetReady(false)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 after SetReady(false), got %d", rec.Code)
	}
}

func TestReadyz_LogsFullErrorWithoutExposingItInResponseBody(t *testing.T) {
	var loggedReason string

	var loggedErr error

	dbErr := errors.New("dial tcp 10.0.0.5:5432: connection refused")

	s := httpserver.New(func(context.Context) error {
		return dbErr
	}, okCheck, func(reason string, err error) {
		loggedReason = reason
		loggedErr = err
	})
	s.SetReady(true)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}

	if strings.Contains(rec.Body.String(), dbErr.Error()) {
		t.Errorf("expected the response body not to contain the raw error, got %q", rec.Body.String())
	}

	if loggedReason != "database unreachable" || !errors.Is(loggedErr, dbErr) {
		t.Errorf("expected the full error to be reported via the logger, got reason=%q err=%v", loggedReason, loggedErr)
	}
}

func doRequest(t *testing.T, s *httpserver.Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	return rec
}

func TestMountWebhook_RoutesDeliveriesToTheHandler(t *testing.T) {
	srv := httpserver.New(okCheck, okCheck, nil)

	var called bool

	srv.MountWebhook(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true

		w.WriteHeader(http.StatusOK)
	}))

	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodPost, httpserver.WebhookPath, nil))

	if !called {
		t.Error("expected the delivery to reach the mounted handler")
	}

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.Code)
	}
}

func TestMountWebhook_RejectsMethodsGitLabNeverUses(t *testing.T) {
	srv := httpserver.New(okCheck, okCheck, nil)
	srv.MountWebhook(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("expected a GET not to reach the webhook handler")

		w.WriteHeader(http.StatusOK)
	}))

	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, httpserver.WebhookPath, nil))

	if resp.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for a method GitLab never delivers with, got %d", resp.Code)
	}
}

func TestHandler_WebhookPathIsNotServedWhenNothingIsMounted(t *testing.T) {
	srv := httpserver.New(okCheck, okCheck, nil)

	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodPost, httpserver.WebhookPath, nil))

	if resp.Code != http.StatusNotFound {
		t.Errorf("expected 404 with no receiver mounted, got %d", resp.Code)
	}
}
