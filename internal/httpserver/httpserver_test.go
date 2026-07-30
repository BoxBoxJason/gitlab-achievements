package httpserver_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/httpserver"
)

func okCheck(context.Context) error { return nil }

func TestHealthz_AlwaysOK(t *testing.T) {
	s := httpserver.New(func(context.Context) error {
		return errors.New("db down")
	}, func(context.Context) error {
		return errors.New("gitlab down")
	})

	rec := doRequest(t, s, http.MethodGet, "/healthz")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestReadyz_NotReadyBeforeSetReady(t *testing.T) {
	s := httpserver.New(okCheck, okCheck)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 before SetReady(true), got %d", rec.Code)
	}
}

func TestReadyz_ReadyWhenChecksPass(t *testing.T) {
	s := httpserver.New(okCheck, okCheck)
	s.SetReady(true)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestReadyz_UnreadyWhenDBCheckFails(t *testing.T) {
	s := httpserver.New(func(context.Context) error {
		return errors.New("db down")
	}, okCheck)
	s.SetReady(true)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when the db check fails, got %d", rec.Code)
	}
}

func TestReadyz_UnreadyWhenGitLabCheckFails(t *testing.T) {
	s := httpserver.New(okCheck, func(context.Context) error {
		return errors.New("gitlab down")
	})
	s.SetReady(true)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when the gitlab check fails, got %d", rec.Code)
	}
}

func TestReadyz_CanFlipBackToUnready(t *testing.T) {
	s := httpserver.New(okCheck, okCheck)
	s.SetReady(true)
	s.SetReady(false)

	rec := doRequest(t, s, http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 after SetReady(false), got %d", rec.Code)
	}
}

func doRequest(t *testing.T, s *httpserver.Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	return rec
}
