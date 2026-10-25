package frontend_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

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

	handler, err := frontend.Handler(dir, frontend.Options{})
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

// TestHandler_StaysInsideItsDirectory covers what an operator's directory
// may hold besides the build: a symlink leading out of it, and dotfiles
// such as an .env copied in along with everything else.
func TestHandler_StaysInsideItsDirectory(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside the root"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	dir := t.TempDir()

	for name, body := range map[string]string{
		"index.html":  "<!doctype html><title>Rankings</title>",
		".env":        "SECRET=1",
		".git/config": "[core]",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	handler, err := frontend.Handler(dir, frontend.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, path := range []string{"/link.txt", "/.env", "/.git/config"} {
		rec := fetch(handler, path)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "outside") || strings.Contains(rec.Body.String(), "SECRET") {
			t.Errorf("GET %s: expected 404, got %d %q", path, rec.Code, rec.Body.String())
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
	if _, err := frontend.Handler(t.TempDir(), frontend.Options{}); err == nil {
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

func TestHandlerFS_CachesHashedAssetsForGoodAndSendsThePolicy(t *testing.T) {
	root := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>Rankings</title>")},
		"assets/app.3f.js": {Data: []byte("console.log('app')")},
		"logo.svg":         {Data: []byte("<svg/>")},
	}

	handler, err := frontend.HandlerFS(root, frontend.Options{ContentSecurityPolicy: "default-src 'self'", ImmutableDir: "assets"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	asset := fetch(handler, "/assets/app.3f.js")
	if got := asset.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("expected a hashed asset to be cached for good, got %q", got)
	}

	if got := fetch(handler, "/logo.svg").Header().Get("Cache-Control"); got != "" {
		t.Errorf("expected a file outside the hashed directory to be left to revalidation, got %q", got)
	}

	if got := fetch(handler, "/").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("expected the index never to be cached, got %q", got)
	}

	if got := fetch(handler, "/users/alice").Header().Get("Content-Security-Policy"); got != "default-src 'self'" {
		t.Errorf("expected the policy on every response, got %q", got)
	}
}

func TestHasIndex(t *testing.T) {
	if frontend.HasIndex(fstest.MapFS{".gitkeep": {}}) {
		t.Error("expected a tree with no index.html not to count as a frontend")
	}

	if !frontend.HasIndex(fstest.MapFS{"index.html": {}}) {
		t.Error("expected a tree with an index.html to count as a frontend")
	}
}
