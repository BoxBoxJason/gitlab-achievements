package gitlabclient_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// identityServer stands in for a GitLab instance's /api/v4/user endpoint.
func identityServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return srv
}

func TestVerify_ResolvesTheIdentityBehindAToken(t *testing.T) {
	srv := identityServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/user" {
			t.Errorf("expected the identity endpoint, got %q", r.URL.Path)
		}

		if got := r.Header.Get("Authorization"); got != "Bearer the-token" {
			t.Errorf("expected the token on the Authorization header, got %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 42, "username": "alice", "is_admin": true}`)) //nolint:errcheck // test server
	})

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	identity, err := verifier.Verify(t.Context(), "the-token")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if identity.ID != 42 || identity.Username != "alice" || !identity.IsAdmin {
		t.Errorf("unexpected identity: %+v", identity)
	}
}

// A refusal and an outage mean opposite things to a caller, so they must
// not come back as the same error.
func TestVerify_ReportsARefusedTokenDistinctly(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		})

		verifier, err := gitlabclient.NewTokenVerifier(srv.URL)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}

		_, err = verifier.Verify(t.Context(), "stale")
		if !errors.Is(err, gitlabclient.ErrInvalidToken) {
			t.Errorf("expected ErrInvalidToken for status %d, got: %v", status, err)
		}
	}
}

func TestVerify_DoesNotReportAServerErrorAsARefusal(t *testing.T) {
	srv := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	_, err = verifier.Verify(t.Context(), "fine")
	if err == nil {
		t.Fatal("expected an error")
	}

	if errors.Is(err, gitlabclient.ErrInvalidToken) {
		t.Errorf("expected a 500 not to be reported as a bad token, got: %v", err)
	}
}

// A 200 carrying no user is not an identity; treating it as one would
// authenticate the caller as user zero.
func TestVerify_RejectsAResponseWithNoUserID(t *testing.T) {
	srv := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"username": "nobody"}`)) //nolint:errcheck // test server
	})

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	_, err = verifier.Verify(t.Context(), "odd")
	if !errors.Is(err, gitlabclient.ErrInvalidToken) {
		t.Errorf("expected a user-less response to be refused, got: %v", err)
	}
}

func TestVerify_RejectsAnEmptyTokenWithoutCallingGitLab(t *testing.T) {
	var called bool

	srv := identityServer(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true

		w.Write([]byte(`{"id": 1}`)) //nolint:errcheck // test server
	})

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	_, err = verifier.Verify(t.Context(), "   ")
	if !errors.Is(err, gitlabclient.ErrInvalidToken) {
		t.Errorf("expected an empty token to be refused, got: %v", err)
	}

	if called {
		t.Error("expected GitLab not to be called for an empty token")
	}
}

func TestVerify_ReportsAnUnreachableInstance(t *testing.T) {
	// A port nothing is listening on, so the request fails at the transport.
	verifier, err := gitlabclient.NewTokenVerifier("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	_, err = verifier.Verify(t.Context(), "token")
	if err == nil {
		t.Fatal("expected an error")
	}

	if errors.Is(err, gitlabclient.ErrInvalidToken) {
		t.Errorf("expected an unreachable instance not to look like a bad token, got: %v", err)
	}
}

func TestNewTokenVerifier_RejectsAnEmptyBaseURL(t *testing.T) {
	if _, err := gitlabclient.NewTokenVerifier("  "); err == nil {
		t.Error("expected an error for an empty base URL")
	}
}

// A trailing slash on the configured URL must not produce a double slash
// in the path.
func TestNewTokenVerifier_TrimsATrailingSlash(t *testing.T) {
	var path string

	srv := identityServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path

		w.Write([]byte(`{"id": 1}`)) //nolint:errcheck // test server
	})

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL + "/")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if _, err := verifier.Verify(t.Context(), "token"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if path != "/api/v4/user" {
		t.Errorf("expected /api/v4/user, got %q", path)
	}
}
