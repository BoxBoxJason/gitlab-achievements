package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

func TestCachingVerifier_AsksGitLabOncePerWindow(t *testing.T) {
	inner := &stubVerifier{identity: &gitlabclient.Identity{ID: 1, Username: "alice"}}
	now := time.Now()

	cache, ok := newCachingVerifier(inner, time.Minute).(*cachingVerifier)
	if !ok {
		t.Fatal("expected a caching verifier for a positive TTL")
	}

	cache.now = func() time.Time { return now }

	for range 5 {
		identity, err := cache.Verify(t.Context(), "token")
		if err != nil || identity.Username != "alice" {
			t.Fatalf("unexpected result: %+v, %v", identity, err)
		}
	}

	if inner.calls != 1 {
		t.Fatalf("expected one GitLab call inside the window, got %d", inner.calls)
	}

	now = now.Add(time.Minute)

	_, _ = cache.Verify(t.Context(), "token")

	if inner.calls != 2 {
		t.Fatalf("expected the identity to be asked about again once the window passed, got %d", inner.calls)
	}

	cache.forget("token")

	_, _ = cache.Verify(t.Context(), "token")

	if inner.calls != 3 {
		t.Fatalf("expected a forgotten token to be asked about again, got %d", inner.calls)
	}
}

func TestCachingVerifier_NeverCachesFailures(t *testing.T) {
	inner := &stubVerifier{err: gitlabclient.ErrInvalidToken}
	cache := newCachingVerifier(inner, time.Minute)

	for range 3 {
		if _, err := cache.Verify(t.Context(), "bad"); !errors.Is(err, gitlabclient.ErrInvalidToken) {
			t.Fatalf("expected the rejection passed through, got %v", err)
		}
	}

	if inner.calls != 3 {
		t.Fatalf("expected every failure to be asked about again, got %d calls", inner.calls)
	}
}

func TestCachingVerifier_StaysBounded(t *testing.T) {
	cache, _ := newCachingVerifier(&stubVerifier{identity: &gitlabclient.Identity{ID: 1}}, time.Hour).(*cachingVerifier)

	for index := range identityCacheEntries + 50 {
		_, _ = cache.Verify(t.Context(), "token-"+string(rune(index)))
	}

	if len(cache.entries) > identityCacheEntries {
		t.Fatalf("expected at most %d entries, got %d", identityCacheEntries, len(cache.entries))
	}
}

func TestCachingVerifier_DisabledWithAZeroTTL(t *testing.T) {
	inner := &stubVerifier{identity: &gitlabclient.Identity{ID: 1}}

	if got := newCachingVerifier(inner, 0); got != Verifier(inner) {
		t.Fatal("expected a zero TTL to leave the verifier uncached")
	}
}

func groupAPI(t *testing.T, identity *gitlabclient.Identity, isMember MembershipChecker) *API {
	t.Helper()

	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 10)

	return mustNew(conn, Options{Verifier: &stubVerifier{identity: identity}, IsMember: isMember})
}

func bearer(req *http.Request) {
	req.Header.Set("Authorization", "Bearer token")
}

func TestAllowedGroup_AdmitsMembersAndAdminsOnly(t *testing.T) {
	members := map[int64]bool{1: true}
	isMember := func(_ context.Context, id int64) (bool, error) { return members[id], nil }

	if rec := get(t, groupAPI(t, &gitlabclient.Identity{ID: 1}, isMember), "/api/v1/leaderboard", bearer); rec.Code != http.StatusOK {
		t.Errorf("expected a member to be served, got %d", rec.Code)
	}

	if rec := get(t, groupAPI(t, &gitlabclient.Identity{ID: 2}, isMember), "/api/v1/leaderboard", bearer); rec.Code != http.StatusForbidden {
		t.Errorf("expected a non-member to be refused with 403, got %d", rec.Code)
	}

	if rec := get(t, groupAPI(t, &gitlabclient.Identity{ID: 3, IsAdmin: true}, isMember), "/api/v1/leaderboard", bearer); rec.Code != http.StatusOK {
		t.Errorf("expected an instance admin to be served, got %d", rec.Code)
	}

	failing := func(context.Context, int64) (bool, error) { return false, errors.New("gitlab down") }
	if rec := get(t, groupAPI(t, &gitlabclient.Identity{ID: 1}, failing), "/api/v1/leaderboard", bearer); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected an unanswerable membership check to be 503, got %d", rec.Code)
	}
}

func TestMe_DescribesTheCaller(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 1350)

	policy, _ := exclusion.NewPolicy(true, nil, "")
	api := mustNew(conn, Options{
		Verifier:   &stubVerifier{identity: &gitlabclient.Identity{ID: 42, Username: "alice"}},
		Exclusions: exclusion.NewFilter(conn, policy, nil, true),
	})

	if rec := get(t, api, MePath); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when not signed in, got %d", rec.Code)
	}

	rec := get(t, api, MePath, bearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	me := decode[Me](t, rec)
	if me.Username != "alice" || me.GitLabUserID != 42 || me.Record == nil || me.Record.ExpTotal != 1350 || me.Record.Rank != 1 {
		t.Fatalf("unexpected identity: %+v", me)
	}

	if me.Participation == nil || me.Participation.Excluded || !me.Participation.Allowed {
		t.Fatalf("unexpected participation: %+v", me.Participation)
	}
}

func TestMe_SignedInWithNothingEarnedIsNotAnError(t *testing.T) {
	api := mustNew(testConn(t), Options{Verifier: &stubVerifier{identity: &gitlabclient.Identity{ID: 9, Username: "new"}}})

	rec := get(t, api, MePath, bearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if me := decode[Me](t, rec); me.Record != nil || me.Username != "new" {
		t.Fatalf("expected a null record for a newcomer, got %+v", me)
	}
}

func TestMe_AbsentWithoutAuthentication(t *testing.T) {
	if rec := get(t, openAPI(testConn(t)), MePath); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when the instance has no sign-in, got %d", rec.Code)
	}
}

func TestLogout_ForgetsTheCachedIdentity(t *testing.T) {
	inner := &stubVerifier{identity: &gitlabclient.Identity{ID: 1}}

	api := mustNew(testConn(t), Options{
		Verifier:         inner,
		IdentityCacheTTL: time.Hour,
		OAuth:            &OAuthOptions{GitLabURL: "https://gitlab.example.com", PublicURL: "https://achievements.example.com", ClientID: "id"},
	})

	id, _, err := api.sessions.create(t.Context(), sessionTokens{AccessToken: "session-token", RefreshToken: "r"})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	cache, _ := api.auth.verifier.(*cachingVerifier)
	_, _ = cache.Verify(t.Context(), "session-token")

	req := httptest.NewRequest(http.MethodPost, LogoutPath, nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: id})
	api.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if _, ok := cache.lookup(sha256Of("session-token")); ok {
		t.Fatal("expected logout to drop the session's token from the identity cache")
	}
}
