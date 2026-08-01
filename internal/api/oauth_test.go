package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

func TestSessions_RoundTripsAToken(t *testing.T) {
	s := &sessions{conn: testConn(t)}

	id, err := s.create(t.Context(), "gitlab-token", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	got, err := s.token(t.Context(), id)
	if err != nil {
		t.Fatalf("expected the session to resolve, got: %v", err)
	}

	if got != "gitlab-token" {
		t.Errorf("expected the stored token back, got %q", got)
	}
}

// A guessable session identifier is a session anyone can take.
func TestSessions_IdentifiersAreUnpredictable(t *testing.T) {
	s := &sessions{conn: testConn(t)}
	seen := make(map[string]bool)

	for range 100 {
		id, err := s.create(t.Context(), "token", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}

		if seen[id] {
			t.Fatalf("expected unique session identifiers, saw %q twice", id)
		}

		seen[id] = true
	}
}

// Expiry is enforced in the query, so an aged-out session is refused even
// if pruning has not yet run.
func TestSessions_RefusesAnExpiredSessionBeforePruning(t *testing.T) {
	s := &sessions{conn: testConn(t)}

	id, err := s.create(t.Context(), "token", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// A past expiry falls back to the default TTL, so force the row to be
	// genuinely stale rather than relying on that.
	if err := s.conn.Model(&appdb.Session{}).Where("id = ?", id).Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("failed to age the session: %v", err)
	}

	if _, err := s.token(t.Context(), id); !errors.Is(err, errNoSession) {
		t.Errorf("expected an expired session to be refused, got: %v", err)
	}
}

func TestSessions_DestroyEndsTheSession(t *testing.T) {
	s := &sessions{conn: testConn(t)}

	id, err := s.create(t.Context(), "token", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if err := s.destroy(t.Context(), id); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if _, err := s.token(t.Context(), id); !errors.Is(err, errNoSession) {
		t.Errorf("expected the session to be gone, got: %v", err)
	}
}

// Logging out twice, or out of a session that expired in between, should
// leave the caller logged out rather than erroring.
func TestSessions_DestroyIsIdempotent(t *testing.T) {
	s := &sessions{conn: testConn(t)}

	if err := s.destroy(t.Context(), "never-existed"); err != nil {
		t.Errorf("expected deleting a missing session to be a no-op, got: %v", err)
	}
}

func TestSessions_PruneRemovesOnlyExpiredRows(t *testing.T) {
	s := &sessions{conn: testConn(t)}

	live, err := s.create(t.Context(), "live", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	dead, err := s.create(t.Context(), "dead", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if err := s.conn.Model(&appdb.Session{}).Where("id = ?", dead).Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("failed to age the session: %v", err)
	}

	pruned, err := s.prune(t.Context())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if pruned != 1 {
		t.Errorf("expected one session pruned, got %d", pruned)
	}

	if _, err := s.token(t.Context(), live); err != nil {
		t.Errorf("expected the live session to survive, got: %v", err)
	}
}

// testOAuthAPI builds an API with the login flow wired up.
func testOAuthAPI(t *testing.T, conn *gorm.DB, verifier Verifier) *API {
	t.Helper()

	return New(conn, Options{
		Verifier: verifier,
		OAuth: &OAuthOptions{
			GitLabURL: "https://gitlab.example.com",
			PublicURL: "https://achievements.example.com",
			ClientID:  "client-id",
		},
	})
}

func TestLogin_RedirectsToGitLabWithPKCEAndState(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	rec := get(t, api, LoginPath)
	if rec.Code != http.StatusFound {
		t.Fatalf("expected a redirect, got %d", rec.Code)
	}

	target, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("failed to parse the redirect: %v", err)
	}

	if target.Host != "gitlab.example.com" || target.Path != "/oauth/authorize" {
		t.Errorf("expected a redirect to GitLab's authorize endpoint, got %q", target)
	}

	query := target.Query()
	if query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
		t.Errorf("expected an S256 PKCE challenge, got %v", query)
	}

	if query.Get("state") == "" {
		t.Error("expected a state nonce")
	}

	if query.Get("redirect_uri") != "https://achievements.example.com"+CallbackPath {
		t.Errorf("unexpected redirect_uri %q", query.Get("redirect_uri"))
	}

	if query.Get("scope") != "read_user" {
		t.Errorf("expected to request read_user only, got %q", query.Get("scope"))
	}
}

// The state nonce and the PKCE verifier are what tie the callback to the
// browser that started the flow, and neither is any use to a script.
func TestLogin_StashesFlowStateInHttpOnlyCookies(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	rec := get(t, api, LoginPath)

	found := map[string]*http.Cookie{}
	for _, cookie := range rec.Result().Cookies() {
		found[cookie.Name] = cookie
	}

	for _, name := range []string{stateCookie, verifierCookie} {
		cookie, ok := found[name]
		if !ok {
			t.Fatalf("expected a %s cookie", name)
		}

		if !cookie.HttpOnly {
			t.Errorf("expected %s to be HttpOnly", name)
		}

		if !cookie.Secure {
			t.Errorf("expected %s to be Secure behind an https public URL", name)
		}
	}
}

// A Secure cookie is never sent back over plain http, so marking one on an
// http deployment would leave the flow unable to ever complete.
func TestLogin_DoesNotMarkCookiesSecureOnAPlainHTTPDeployment(t *testing.T) {
	api := New(testConn(t), Options{
		Verifier: &stubVerifier{identity: &gitlabclient.Identity{ID: 1}},
		OAuth: &OAuthOptions{
			GitLabURL: "http://gitlab.internal",
			PublicURL: "http://achievements.internal",
			ClientID:  "client-id",
		},
	})

	rec := get(t, api, LoginPath)
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Secure {
			t.Errorf("expected %s not to be Secure on an http deployment", cookie.Name)
		}
	}
}

func TestCallback_RejectsAStateMismatch(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	rec := get(t, api, CallbackPath+"?code=abc&state=attacker", func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: stateCookie, Value: "the-real-one"})
		req.AddCookie(&http.Cookie{Name: verifierCookie, Value: "verifier"})
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a mismatched state, got %d", rec.Code)
	}
}

func TestCallback_RejectsACallbackWithNoFlowInProgress(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	rec := get(t, api, CallbackPath+"?code=abc&state=whatever")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with no state cookie, got %d", rec.Code)
	}
}

func TestCallback_RejectsACallbackCarryingNoCode(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	rec := get(t, api, CallbackPath+"?state=nonce", func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: stateCookie, Value: "nonce"})
		req.AddCookie(&http.Cookie{Name: verifierCookie, Value: "verifier"})
	})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with no authorization code, got %d", rec.Code)
	}
}

// GitLab reports a refusal in the query string alongside a 200, so it has
// to be checked before anything else in the callback is trusted.
func TestCallback_ReportsGitLabsRefusal(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	rec := get(t, api, CallbackPath+"?error=access_denied")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when GitLab refuses the login, got %d", rec.Code)
	}
}

// A failed attempt must not leave a verifier behind that could be paired
// with a later code.
func TestCallback_ClearsFlowCookiesOnFailure(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	rec := get(t, api, CallbackPath+"?code=abc&state=wrong", func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: stateCookie, Value: "right"})
		req.AddCookie(&http.Cookie{Name: verifierCookie, Value: "verifier"})
	})

	cleared := map[string]bool{}

	for _, cookie := range rec.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = true
		}
	}

	for _, name := range []string{stateCookie, verifierCookie} {
		if !cleared[name] {
			t.Errorf("expected %s to be cleared after a failed callback", name)
		}
	}
}

func TestLogout_ClearsTheSessionAndItsCookie(t *testing.T) {
	conn := testConn(t)
	api := testOAuthAPI(t, conn, &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	id, err := api.sessions.create(t.Context(), "token", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, LogoutPath, nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: id})

	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	if _, err := api.sessions.token(t.Context(), id); !errors.Is(err, errNoSession) {
		t.Errorf("expected the session to be gone, got: %v", err)
	}

	var cleared bool

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie && cookie.MaxAge < 0 {
			cleared = true
		}
	}

	if !cleared {
		t.Error("expected the session cookie to be cleared")
	}
}

// Logging out changes state, so it must not be reachable by a link or a
// prefetch.
func TestLogout_RejectsAGET(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	if rec := get(t, api, LogoutPath); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for a GET logout, got %d", rec.Code)
	}
}

// The login routes are how a browser acquires a credential, so requiring
// one to reach them would be a deadlock.
func TestOAuthRoutes_AreNotBehindAuthentication(t *testing.T) {
	api := testOAuthAPI(t, testConn(t), &stubVerifier{err: gitlabclient.ErrInvalidToken})

	if rec := get(t, api, LoginPath); rec.Code != http.StatusFound {
		t.Errorf("expected login to be reachable without a credential, got %d", rec.Code)
	}
}

// Without OAuth configured there is no login flow, and the routes should
// simply not be there rather than half-answer.
func TestOAuthRoutes_AbsentWhenNotConfigured(t *testing.T) {
	api := New(testConn(t), Options{Verifier: &stubVerifier{identity: &gitlabclient.Identity{ID: 1}}})

	if rec := get(t, api, LoginPath); rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 with no OAuth configured, got %d", rec.Code)
	}
}

func TestSessionCookieAge_FallsBackWhenGitLabGivesNoExpiry(t *testing.T) {
	if got := sessionCookieAge(time.Time{}); got != int(defaultSessionTTL.Seconds()) {
		t.Errorf("expected the default TTL, got %d", got)
	}

	if got := sessionCookieAge(time.Now().Add(-time.Hour)); got != int(defaultSessionTTL.Seconds()) {
		t.Errorf("expected the default TTL for an already-past expiry, got %d", got)
	}
}

func TestNewOAuthFlow_BuildsGitLabEndpointsFromTheBaseURL(t *testing.T) {
	flow := newOAuthFlow(OAuthOptions{
		GitLabURL: "https://gitlab.example.com/",
		PublicURL: "https://achievements.example.com/",
		ClientID:  "id",
	}, &sessions{conn: testConn(t)}, &stubVerifier{})

	if !strings.HasSuffix(flow.config.Endpoint.AuthURL, "/oauth/authorize") ||
		strings.Contains(flow.config.Endpoint.AuthURL, "//oauth") {
		t.Errorf("unexpected authorize URL %q", flow.config.Endpoint.AuthURL)
	}

	if flow.config.RedirectURL != "https://achievements.example.com"+CallbackPath {
		t.Errorf("unexpected redirect URL %q", flow.config.RedirectURL)
	}
}
