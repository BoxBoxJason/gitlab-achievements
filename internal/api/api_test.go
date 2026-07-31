package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// stubVerifier stands in for GitLab during authentication tests.
type stubVerifier struct {
	identity *gitlabclient.Identity
	err      error
	calls    int
}

func (v *stubVerifier) Verify(context.Context, string) (*gitlabclient.Identity, error) {
	v.calls++

	if v.err != nil {
		return nil, v.err
	}

	return v.identity, nil
}

// get issues a GET against an API and returns the recorder.
func get(t *testing.T, api *API, path string, decorate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, d := range decorate {
		d(req)
	}

	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	return rec
}

// decode unmarshals a response body, failing the test if it isn't JSON.
func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var payload T
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response %q: %v", rec.Body.String(), err)
	}

	return payload
}

// openAPI builds an unauthenticated API over conn.
func openAPI(conn *gorm.DB) *API {
	return New(conn, Options{})
}

func TestHandleUserEXP_ServesTheTotal(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 1350)

	rec := get(t, openAPI(conn), "/api/v1/users/alice/exp")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	got := decode[Summary](t, rec)
	if got.ExpTotal != 1350 || got.GitLabUserID != 42 || got.Username != "alice" {
		t.Errorf("unexpected payload: %+v", got)
	}
}

func TestHandleUser_ServesTheWholeRecord(t *testing.T) {
	conn := testConn(t)
	userID := seedUser(t, conn, 42, "alice", 300)
	seedCounter(t, conn, userID, "commits", 412)
	seedAward(t, conn, userID, "commits", 3, 100, 300, appdb.AwardStatusAccepted)

	rec := get(t, openAPI(conn), "/api/v1/users/42")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	got := decode[Detail](t, rec)
	if got.ExpTotal != 300 || len(got.Counters) != 1 || len(got.Awards) != 1 {
		t.Errorf("unexpected payload: %+v", got)
	}
}

// The 404 and the zero total mean different things, and a caller has to be
// able to tell which it got.
func TestHandleUser_DistinguishesUnknownFromZero(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "known", 0)

	api := openAPI(conn)

	if rec := get(t, api, "/api/v1/users/known/exp"); rec.Code != http.StatusOK {
		t.Errorf("expected 200 for a known user with no EXP, got %d", rec.Code)
	}

	if rec := get(t, api, "/api/v1/users/stranger/exp"); rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for a user never seen, got %d", rec.Code)
	}
}

func TestHandleLeaderboard_DefaultsAndValidatesTheLimit(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 1, "alice", 10)

	api := openAPI(conn)

	rec := get(t, api, "/api/v1/leaderboard")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if got := decode[Leaderboard](t, rec); got.Limit != defaultLeaderboardLimit {
		t.Errorf("expected the default limit, got %d", got.Limit)
	}

	for _, bad := range []string{"0", "-1", "abc", "101"} {
		if rec := get(t, api, "/api/v1/leaderboard?limit="+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for limit=%q, got %d", bad, rec.Code)
		}
	}
}

func TestErrorResponses_AreJSON(t *testing.T) {
	rec := get(t, openAPI(testConn(t)), "/api/v1/users/nobody/exp")

	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected a JSON content type, got %q", ct)
	}

	if got := decode[errorBody](t, rec); got.Error == "" {
		t.Error("expected an error message in the body")
	}
}

// With no verifier configured the API is open, which is the default
// posture and has to stay that way for an existing deployment.
func TestAuth_OpenWhenNoVerifierIsConfigured(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 10)

	if rec := get(t, openAPI(conn), "/api/v1/users/alice/exp"); rec.Code != http.StatusOK {
		t.Errorf("expected 200 without a credential, got %d", rec.Code)
	}
}

func authenticatedAPI(t *testing.T, conn *gorm.DB, verifier Verifier) *API {
	t.Helper()

	return New(conn, Options{Verifier: verifier})
}

func TestAuth_RejectsARequestWithNoCredential(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 10)

	verifier := &stubVerifier{identity: &gitlabclient.Identity{ID: 1, Username: "caller"}}

	rec := get(t, authenticatedAPI(t, conn, verifier), "/api/v1/users/alice/exp")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without a credential, got %d", rec.Code)
	}

	if verifier.calls != 0 {
		t.Errorf("expected GitLab not to be consulted without a credential, got %d calls", verifier.calls)
	}
}

func TestAuth_AcceptsABearerToken(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 10)

	verifier := &stubVerifier{identity: &gitlabclient.Identity{ID: 1, Username: "caller"}}

	rec := get(t, authenticatedAPI(t, conn, verifier), "/api/v1/users/alice/exp", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer glpat-something")
	})

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with a valid token, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// Any authenticated GitLab identity may read anybody's record: the thing
// being prevented is anonymous enumeration, not user-to-user reads.
func TestAuth_AnyIdentityMayReadAnyUser(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 10)

	verifier := &stubVerifier{identity: &gitlabclient.Identity{ID: 999, Username: "somebody-else"}}

	rec := get(t, authenticatedAPI(t, conn, verifier), "/api/v1/users/alice", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer token")
	})

	if rec.Code != http.StatusOK {
		t.Errorf("expected any authenticated caller to read any user, got %d", rec.Code)
	}
}

func TestAuth_RejectsAMalformedAuthorizationHeader(t *testing.T) {
	conn := testConn(t)
	verifier := &stubVerifier{identity: &gitlabclient.Identity{ID: 1}}

	for _, header := range []string{"token-without-scheme", "Bearer ", "Basic abc"} {
		rec := get(t, authenticatedAPI(t, conn, verifier), "/api/v1/users/alice/exp", func(req *http.Request) {
			req.Header.Set("Authorization", header)
		})

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 for header %q, got %d", header, rec.Code)
		}
	}
}

// A token GitLab refuses is the caller's problem: 401, and they should go
// and fix their token.
func TestAuth_RejectedTokenIs401(t *testing.T) {
	conn := testConn(t)
	verifier := &stubVerifier{err: gitlabclient.ErrInvalidToken}

	rec := get(t, authenticatedAPI(t, conn, verifier), "/api/v1/users/alice/exp", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer stale")
	})

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for a token GitLab rejected, got %d", rec.Code)
	}
}

// A token that could not be checked at all is this app's problem: 503, not
// 401, so nobody is told to rotate a credential that is perfectly good.
func TestAuth_UnreachableGitLabIs503(t *testing.T) {
	conn := testConn(t)
	verifier := &stubVerifier{err: errors.New("dial tcp: connection refused")}

	rec := get(t, authenticatedAPI(t, conn, verifier), "/api/v1/users/alice/exp", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer fine")
	})

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when GitLab cannot be reached, got %d", rec.Code)
	}
}

// GitLab's own error can name internal hosts, and the caller can do
// nothing with it.
func TestAuth_DoesNotLeakGitLabsErrorToTheCaller(t *testing.T) {
	conn := testConn(t)
	verifier := &stubVerifier{err: errors.New("dial tcp 10.0.0.5:443: connection refused")}

	rec := get(t, authenticatedAPI(t, conn, verifier), "/api/v1/users/alice/exp", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer fine")
	})

	if body := rec.Body.String(); strings.Contains(body, "10.0.0.5") {
		t.Errorf("expected the upstream error to stay out of the body, got %q", body)
	}
}

func TestAuth_AcceptsASessionCookie(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 10)

	verifier := &stubVerifier{identity: &gitlabclient.Identity{ID: 1, Username: "caller"}}
	api := authenticatedAPI(t, conn, verifier)

	id, err := api.sessions.create(t.Context(), "gitlab-access-token", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	rec := get(t, api, "/api/v1/users/alice/exp", func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: id})
	})

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with a session cookie, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAuth_RejectsAnUnknownSessionCookie(t *testing.T) {
	conn := testConn(t)
	verifier := &stubVerifier{identity: &gitlabclient.Identity{ID: 1}}

	rec := get(t, authenticatedAPI(t, conn, verifier), "/api/v1/users/alice/exp", func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "not-a-session"})
	})

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for an unknown session, got %d", rec.Code)
	}
}

// An explicit Authorization header must win, so a caller who sends one is
// never silently authenticated as whoever the browser is logged in as.
func TestAuth_PrefersTheHeaderOverTheCookie(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 10)

	verifier := &recordingVerifier{identity: &gitlabclient.Identity{ID: 1}}
	api := authenticatedAPI(t, conn, verifier)

	id, err := api.sessions.create(t.Context(), "session-token", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("failed to create a session: %v", err)
	}

	get(t, api, "/api/v1/users/alice/exp", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer header-token")
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: id})
	})

	if verifier.lastToken != "header-token" {
		t.Errorf("expected the header's token to be used, got %q", verifier.lastToken)
	}
}

// recordingVerifier remembers which credential it was handed.
type recordingVerifier struct {
	identity  *gitlabclient.Identity
	lastToken string
}

func (v *recordingVerifier) Verify(_ context.Context, token string) (*gitlabclient.Identity, error) {
	v.lastToken = token

	return v.identity, nil
}
