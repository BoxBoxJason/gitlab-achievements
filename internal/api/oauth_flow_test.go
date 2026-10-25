package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// fakeGitLab answers GitLab's token endpoint: authorization codes and
// refresh tokens alike, or refuses refreshes when told to. Its revocation
// endpoint accepts everything.
func fakeGitLab(t *testing.T, refuseRefresh bool) *httptest.Server {
	t.Helper()

	server, _ := revocationRecordingGitLab(t, refuseRefresh)

	return server
}

// revocationRecordingGitLab is fakeGitLab, also reporting every form posted
// to its revocation endpoint.
func revocationRecordingGitLab(t *testing.T, refuseRefresh bool) (*httptest.Server, func() []url.Values) {
	t.Helper()

	var (
		mu      sync.Mutex
		revoked []url.Values
	)

	server := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()

		if req.URL.Path == "/oauth/revoke" {
			mu.Lock()
			revoked = append(revoked, req.PostForm)
			mu.Unlock()

			return
		}

		if req.URL.Path != "/oauth/token" {
			http.NotFound(resp, req)

			return
		}

		resp.Header().Set("Content-Type", "application/json")

		if req.Form.Get("grant_type") == "refresh_token" && refuseRefresh {
			resp.WriteHeader(http.StatusBadRequest)
			_, _ = resp.Write([]byte(`{"error":"invalid_grant","error_description":"revoked"}`))

			return
		}

		_ = json.NewEncoder(resp).Encode(map[string]any{
			"access_token":  "access-for-" + req.Form.Get("grant_type"),
			"refresh_token": "refresh-" + req.Form.Get("grant_type"),
			"token_type":    "Bearer",
			"expires_in":    7200,
		})
	}))

	t.Cleanup(server.Close)

	return server, func() []url.Values {
		mu.Lock()
		defer mu.Unlock()

		return append([]url.Values(nil), revoked...)
	}
}

// rejectingGitLab answers every token request as GitLab does when the app's
// own OAuth client credentials are wrong.
func rejectingGitLab(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
		resp.Header().Set("Content-Type", "application/json")
		resp.WriteHeader(http.StatusUnauthorized)
		_, _ = resp.Write([]byte(`{"error":"invalid_client","error_description":"Client authentication failed"}`))
	}))

	t.Cleanup(server.Close)

	return server
}

func flowAPI(t *testing.T, gitlabURL string, verifier Verifier) *API {
	t.Helper()

	return mustNew(testConn(t), Options{
		Verifier: verifier,
		OAuth: &OAuthOptions{
			GitLabURL:     gitlabURL,
			PublicURL:     "https://achievements.example.com",
			ClientID:      "client-id",
			ReturnOrigins: []string{"https://rankings.example.com"},
		},
	})
}

// login runs the whole flow and returns the callback's response.
func login(t *testing.T, api *API, returnTo string) *httptest.ResponseRecorder {
	t.Helper()

	path := LoginPath
	if returnTo != "" {
		path += "?return_to=" + url.QueryEscape(returnTo)
	}

	start := get(t, api, path)
	if start.Code != http.StatusFound {
		t.Fatalf("expected the login to redirect to GitLab, got %d (%s)", start.Code, start.Body.String())
	}

	authorize, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatalf("failed to parse redirect: %v", err)
	}

	return get(t, api, CallbackPath+"?code=the-code&state="+url.QueryEscape(authorize.Query().Get("state")),
		func(req *http.Request) {
			for _, cookie := range start.Result().Cookies() {
				req.AddCookie(cookie)
			}
		})
}

func TestLogin_ReturnsToTheRequestedPage(t *testing.T) {
	gitlab := fakeGitLab(t, false)
	api := flowAPI(t, gitlab.URL, &stubVerifier{identity: &gitlabclient.Identity{ID: 42, Username: "alice"}})

	for returnTo, want := range map[string]string{
		"":                                "/api/v1/users/alice",
		"/rankings?tab=week#top":          "/rankings?tab=week#top",
		"https://rankings.example.com/me": "https://rankings.example.com/me",
		"https://achievements.example.com/boards": "https://achievements.example.com/boards",
	} {
		rec := login(t, api, returnTo)
		if rec.Code != http.StatusFound {
			t.Fatalf("return_to %q: expected a redirect, got %d (%s)", returnTo, rec.Code, rec.Body.String())
		}

		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("return_to %q: landed on %q, want %q", returnTo, got, want)
		}
	}
}

func TestLogin_WithoutReturnToLandsWhereConfigured(t *testing.T) {
	gitlab := fakeGitLab(t, false)
	api := mustNew(testConn(t), Options{
		Verifier: &stubVerifier{identity: &gitlabclient.Identity{ID: 42, Username: "alice"}},
		OAuth: &OAuthOptions{
			GitLabURL: gitlab.URL,
			PublicURL: "https://achievements.example.com",
			ClientID:  "client-id",
			Landing:   func(username string) string { return "/users/" + username },
		},
	})

	for returnTo, want := range map[string]string{
		"":        "/users/alice",
		"/boards": "/boards",
	} {
		rec := login(t, api, returnTo)
		if got := rec.Header().Get("Location"); rec.Code != http.StatusFound || got != want {
			t.Errorf("return_to %q: got %d to %q, want a redirect to %q", returnTo, rec.Code, got, want)
		}
	}
}

func TestLogin_WithoutReturnToDropsAnAbandonedLoginsDestination(t *testing.T) {
	api := flowAPI(t, "https://gitlab.example.com", &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	rec := get(t, api, LoginPath)

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == returnCookie && cookie.MaxAge >= 0 {
			t.Fatalf("expected a login without return_to to expire any stashed destination, got %+v", cookie)
		}
	}
}

func TestLogin_AgainEndsThePreviousSession(t *testing.T) {
	gitlab, revoked := revocationRecordingGitLab(t, false)
	api := flowAPI(t, gitlab.URL, &stubVerifier{identity: &gitlabclient.Identity{ID: 42, Username: "alice"}})

	previous, _, err := api.sessions.create(t.Context(), sessionTokens{AccessToken: "old", RefreshToken: "old-refresh"})
	if err != nil {
		t.Fatalf("failed to seed a session: %v", err)
	}

	start := get(t, api, LoginPath)
	authorize, _ := url.Parse(start.Header().Get("Location"))

	rec := get(t, api, CallbackPath+"?code=the-code&state="+url.QueryEscape(authorize.Query().Get("state")),
		func(req *http.Request) {
			for _, cookie := range start.Result().Cookies() {
				req.AddCookie(cookie)
			}

			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: previous})
		})
	if rec.Code != http.StatusFound {
		t.Fatalf("expected the login to finish, got %d (%s)", rec.Code, rec.Body.String())
	}

	_, err = api.sessions.load(t.Context(), previous)
	if !errors.Is(err, errNoSession) {
		t.Fatalf("expected the replaced session to be gone, got %v", err)
	}

	forms := revoked()
	if len(forms) != 1 || forms[0].Get("token") != "old-refresh" || forms[0].Get("token_type_hint") != "refresh_token" {
		t.Errorf("expected the replaced session's refresh token revoked on GitLab, got %v", forms)
	}
}

// Logging out has to end the GitLab credential the session held, not just
// the row: an unrevoked refresh token outlives any copy of the table.
func TestLogout_RevokesTheSessionsTokensOnGitLab(t *testing.T) {
	gitlab, revoked := revocationRecordingGitLab(t, false)
	api := flowAPI(t, gitlab.URL, &stubVerifier{identity: &gitlabclient.Identity{ID: 42, Username: "alice"}})

	var sessionID string

	for _, cookie := range login(t, api, "").Result().Cookies() {
		if cookie.Name == SessionCookie {
			sessionID = cookie.Value
		}
	}

	req := httptest.NewRequest(http.MethodPost, LogoutPath, nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: sessionID})
	api.Handler().ServeHTTP(httptest.NewRecorder(), req)

	forms := revoked()
	if len(forms) != 1 {
		t.Fatalf("expected one revocation, got %v", forms)
	}

	if forms[0].Get("token") != "refresh-authorization_code" || forms[0].Get("client_id") != "client-id" {
		t.Errorf("expected the session's refresh token revoked for this client, got %v", forms[0])
	}
}

func TestLogin_StoresTheRefreshToken(t *testing.T) {
	gitlab := fakeGitLab(t, false)
	api := flowAPI(t, gitlab.URL, &stubVerifier{identity: &gitlabclient.Identity{ID: 42, Username: "alice"}})

	rec := login(t, api, "")

	var sessionID string

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie {
			sessionID = cookie.Value

			if cookie.MaxAge < int(config.DefaultSessionLifetime.Seconds())-60 {
				t.Errorf("expected the cookie to last the session's lifetime, got max-age %d", cookie.MaxAge)
			}
		}
	}

	tokens, err := api.sessions.load(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("expected a session, got %v", err)
	}

	if tokens.AccessToken != "access-for-authorization_code" || tokens.RefreshToken != "refresh-authorization_code" || tokens.Expiry.IsZero() {
		t.Fatalf("expected both tokens and the expiry kept, got %+v", tokens)
	}
}

func TestLogin_RefusesDestinationsThatWouldBeAnOpenRedirect(t *testing.T) {
	api := flowAPI(t, "https://gitlab.example.com", &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	for _, returnTo := range []string{
		"https://evil.example.com/",
		"//evil.example.com/path",
		"/\\evil.example.com",
		"javascript:alert(1)",
		"https://rankings.example.com.evil.example/",
		"https://user@rankings.example.com/",
		"rankings",
		"/ok\r\nSet-Cookie: x=y",
	} {
		rec := get(t, api, LoginPath+"?return_to="+url.QueryEscape(returnTo))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected return_to %q to be refused, got %d", returnTo, rec.Code)
		}
	}
}

func TestLogin_RefusesAnAccountOutsideTheAllowedGroup(t *testing.T) {
	gitlab := fakeGitLab(t, false)

	api := mustNew(testConn(t), Options{
		Verifier: &stubVerifier{identity: &gitlabclient.Identity{ID: 7, Username: "outsider"}},
		IsMember: func(_ context.Context, _ int64) (bool, error) { return false, nil },
		OAuth: &OAuthOptions{
			GitLabURL: gitlab.URL,
			PublicURL: "https://achievements.example.com",
			ClientID:  "client-id",
		},
	})

	rec := login(t, api, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an account outside the group, got %d", rec.Code)
	}

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie && cookie.Value != "" {
			t.Fatal("expected no session for a refused login")
		}
	}
}

func TestOAuthFlow_RefreshAgainstGitLab(t *testing.T) {
	gitlab := fakeGitLab(t, false)
	api := flowAPI(t, gitlab.URL, &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	tokens, err := api.oauth.refresh(t.Context(), "old-refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tokens.AccessToken != "access-for-refresh_token" || tokens.RefreshToken != "refresh-refresh_token" {
		t.Fatalf("expected GitLab's renewed tokens, got %+v", tokens)
	}

	refusing := flowAPI(t, fakeGitLab(t, true).URL, &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	_, err = refusing.oauth.refresh(t.Context(), "revoked")
	if !errors.Is(err, errRefreshRejected) {
		t.Fatalf("expected invalid_grant to end the session, got %v", err)
	}

	misconfigured := flowAPI(t, rejectingGitLab(t).URL, &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	_, err = misconfigured.oauth.refresh(t.Context(), "fine")
	if err == nil || errors.Is(err, errRefreshRejected) {
		t.Fatalf("expected a rejected client not to end the session, got %v", err)
	}

	unreachable := flowAPI(t, "http://127.0.0.1:1", &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	_, err = unreachable.oauth.refresh(t.Context(), "fine")
	if err == nil || errors.Is(err, errRefreshRejected) {
		t.Fatalf("expected an unreachable GitLab to be a transient error, got %v", err)
	}

	if strings.Contains(err.Error(), "fine") {
		t.Error("expected the refresh token to stay out of the error")
	}
}
