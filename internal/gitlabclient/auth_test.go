package gitlabclient_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// clientID is the OAuth application the verifiers under test belong to.
const clientID = "this-app-client-id"

// identityServer stands in for a GitLab instance's /api/v4/user endpoint.
// Its token info endpoint knows none of the tokens, as for a personal
// access token.
func identityServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	return oauthServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}, handler)
}

// oauthServer stands in for a GitLab instance answering /oauth/token/info
// with tokenInfo and everything else with handler.
func oauthServer(t *testing.T, tokenInfo, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token/info" {
			tokenInfo(w, r)

			return
		}

		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// issuedTo answers token info for an OAuth token application uid issued.
func issuedTo(uid string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"resource_owner_id": 42, "scope": ["read_user"], "application": {"uid": "` + uid + `"}}`)) //nolint:errcheck // test server
	}
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

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL, clientID)
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

		verifier, err := gitlabclient.NewTokenVerifier(srv.URL, clientID)
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

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL, clientID)
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

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL, clientID)
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

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL, clientID)
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
	verifier, err := gitlabclient.NewTokenVerifier("http://127.0.0.1:1", clientID)
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

func TestVerify_AcceptsAnOAuthTokenThisAppIssued(t *testing.T) {
	srv := oauthServer(t, issuedTo(clientID), func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id": 42, "username": "alice"}`)) //nolint:errcheck // test server
	})

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL, clientID)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	identity, err := verifier.Verify(t.Context(), "our-oauth-token")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if identity.ID != 42 {
		t.Errorf("unexpected identity: %+v", identity)
	}
}

// GitLab honors an OAuth token from any application on /api/v4/user, so
// one issued to some other relying party has to be refused here or that
// party could act as its users on this API.
func TestVerify_RefusesAnOAuthTokenAnotherApplicationIssued(t *testing.T) {
	var identityAsked bool

	srv := oauthServer(t, issuedTo("other-app-uid"), func(w http.ResponseWriter, _ *http.Request) {
		identityAsked = true

		w.Write([]byte(`{"id": 42, "username": "alice"}`)) //nolint:errcheck // test server
	})

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL, clientID)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	_, err = verifier.Verify(t.Context(), "other-app-oauth-token")
	if !errors.Is(err, gitlabclient.ErrInvalidToken) {
		t.Errorf("expected another application's token to be refused, got: %v", err)
	}

	if identityAsked {
		t.Error("expected the identity not to be looked up for a refused token")
	}
}

// Not knowing which application issued a token is not a verdict on it, so
// a token info outage must not read as a bad token either.
func TestVerify_DoesNotReportATokenInfoOutageAsARefusal(t *testing.T) {
	srv := oauthServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id": 42}`)) //nolint:errcheck // test server
	})

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL, clientID)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	_, err = verifier.Verify(t.Context(), "token")
	if err == nil {
		t.Fatal("expected an error")
	}

	if errors.Is(err, gitlabclient.ErrInvalidToken) {
		t.Errorf("expected a token info outage not to look like a bad token, got: %v", err)
	}
}

func TestNewTokenVerifier_RejectsAnEmptyClientID(t *testing.T) {
	if _, err := gitlabclient.NewTokenVerifier("https://gitlab.example.com", " "); err == nil {
		t.Error("expected an error for an empty client ID")
	}
}

func TestNewTokenVerifier_RejectsAnEmptyBaseURL(t *testing.T) {
	if _, err := gitlabclient.NewTokenVerifier("  ", clientID); err == nil {
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

	verifier, err := gitlabclient.NewTokenVerifier(srv.URL+"/", clientID)
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
