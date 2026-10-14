package frontend_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/frontend"
)

func site(t *testing.T) http.Handler {
	t.Helper()

	dir := t.TempDir()

	for name, body := range map[string]string{
		"index.html":          "<!doctype html><title>Rankings</title>",
		"assets/app.3f9a.js":  "console.log('app')",
		"assets/nested/x.css": "body{}",
		"favicon.ico":         "icon",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	handler, err := frontend.Handler(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	return handler
}

func fetch(handler http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	return rec
}

func TestHandler_ServesFilesAndClientRoutes(t *testing.T) {
	handler := site(t)

	for path, want := range map[string]string{
		"/":                    "Rankings",
		"/users/alice":         "Rankings",
		"/leaderboard/week":    "Rankings",
		"/assets/":             "Rankings",
		"/assets/app.3f9a.js":  "console.log",
		"/assets/nested/x.css": "body{}",
	} {
		rec := fetch(handler, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s: got %d %q, want 200 containing %q", path, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestHandler_MissingAssetsAre404(t *testing.T) {
	handler := site(t)

	for _, path := range []string{"/assets/missing.js", "/robots.txt", "/../../etc/passwd.txt"} {
		if rec := fetch(handler, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: expected 404, got %d", path, rec.Code)
		}
	}
}

func TestHandler_NavigationToADottedRouteGetsTheIndex(t *testing.T) {
	handler := site(t)

	page := httptest.NewRequest(http.MethodGet, "/users/first.last", nil)
	page.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, page)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Rankings") {
		t.Fatalf("navigation to /users/first.last = %d %q, want the index", rec.Code, rec.Body.String())
	}

	if code := fetch(handler, "/users/first.last").Code; code != http.StatusNotFound {
		t.Fatalf("non-navigation fetch of a dotted missing path = %d, want 404", code)
	}
}

func TestHandler_IndexIsNeverCachedAndHeadersAreSet(t *testing.T) {
	handler := site(t)

	index := fetch(handler, "/users/alice")
	if index.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("expected index.html not to be cached, got %q", index.Header().Get("Cache-Control"))
	}

	for _, rec := range []*httptest.ResponseRecorder{index, fetch(handler, "/assets/app.3f9a.js")} {
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("expected security headers, got %v", rec.Header())
		}
	}
}

func TestHandler_RequiresAnIndex(t *testing.T) {
	if _, err := frontend.Handler(t.TempDir()); err == nil {
		t.Fatal("expected a directory with no index.html to be refused")
	}
}

func TestHandler_OnlyAnswersReads(t *testing.T) {
	rec := httptest.NewRecorder()
	site(t).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for a POST, got %d", rec.Code)
	}
}
