package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

func optOutAPI(t *testing.T, conn *gorm.DB, allow bool) *API {
	t.Helper()

	policy, err := exclusion.NewPolicy(true, nil, "")
	if err != nil {
		t.Fatalf("failed to build policy: %v", err)
	}

	return mustNew(conn, Options{
		Verifier:   &stubVerifier{identity: &gitlabclient.Identity{ID: 42, Username: "alice"}},
		Exclusions: exclusion.NewFilter(conn, policy, nil, allow),
	})
}

func send(api *API, method, path string, decorate ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer token")

	for _, d := range decorate {
		d(req)
	}

	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	return rec
}

func TestOptOut_HidesTheUserAndCanBeUndone(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 1350)
	seedUser(t, conn, 17, "bob", 900)

	api := optOutAPI(t, conn, true)

	rec := send(api, http.MethodPost, OptOutPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	status := decode[OptOutStatus](t, rec)
	if !status.OptedOut || !status.Excluded || status.Reason != string(exclusion.OptedOut) || status.OptedOutAt == nil {
		t.Fatalf("unexpected status after opting out: %+v", status)
	}

	if rec := send(api, http.MethodGet, "/api/v1/users/alice"); rec.Code != http.StatusNotFound {
		t.Errorf("expected an opted-out user to be a 404, got %d", rec.Code)
	}

	board := decode[Leaderboard](t, send(api, http.MethodGet, "/api/v1/leaderboard"))
	if len(board.Entries) != 1 || board.Entries[0].Username != "bob" || board.Entries[0].Rank != 1 {
		t.Errorf("expected only bob on the leaderboard, got %+v", board.Entries)
	}

	rec = send(api, http.MethodDelete, OptOutPath)
	if rec.Code != http.StatusOK || decode[OptOutStatus](t, rec).OptedOut {
		t.Fatalf("expected opting back in to succeed, got %d (%s)", rec.Code, rec.Body.String())
	}

	if rec := send(api, http.MethodGet, "/api/v1/users/alice"); rec.Code != http.StatusOK {
		t.Errorf("expected alice to be visible again, got %d", rec.Code)
	}
}

func TestOptOut_RefusedWhenDisabled(t *testing.T) {
	conn := testConn(t)
	api := optOutAPI(t, conn, false)

	if rec := send(api, http.MethodPost, OptOutPath); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}

	status := decode[OptOutStatus](t, send(api, http.MethodGet, OptOutPath))
	if status.Allowed {
		t.Errorf("expected the status to say opting out is not allowed, got %+v", status)
	}
}

func TestOptOut_UnavailableWithoutAuthentication(t *testing.T) {
	conn := testConn(t)

	policy, err := exclusion.NewPolicy(true, nil, "")
	if err != nil {
		t.Fatalf("failed to build policy: %v", err)
	}

	api := mustNew(conn, Options{Exclusions: exclusion.NewFilter(conn, policy, nil, true)})

	if rec := send(api, http.MethodPost, OptOutPath); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without authentication, got %d", rec.Code)
	}
}

func TestOptOut_RefusesACrossOriginBrowserRequest(t *testing.T) {
	conn := testConn(t)
	api := optOutAPI(t, conn, true)

	rec := send(api, http.MethodPost, OptOutPath, func(req *http.Request) {
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example.com")
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected a cross-origin POST to be refused, got %d", rec.Code)
	}

	var user appdb.User
	if err := conn.Where(&appdb.User{GitLabUserID: 42}).First(&user).Error; err == nil && user.OptedOutAt != nil {
		t.Fatal("expected the forged request to have changed nothing")
	}
}

func TestLeaderboard_LeavesOutExcludedUsers(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 1, "alice", 100)
	botID := seedUser(t, conn, 2, "project_5_bot_abc", 9000)

	err := conn.Model(&appdb.User{}).Where("id = ?", botID).Update("excluded_reason", string(exclusion.Bot)).Error
	if err != nil {
		t.Fatalf("failed to mark bot: %v", err)
	}

	board := decode[Leaderboard](t, get(t, openAPI(conn), "/api/v1/leaderboard"))
	if len(board.Entries) != 1 || board.Entries[0].Username != "alice" {
		t.Fatalf("expected the bot to be left off the leaderboard, got %+v", board.Entries)
	}
}
