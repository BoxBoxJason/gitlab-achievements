package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

func TestSealer_RoundTripsAndBindsToTheRow(t *testing.T) {
	sealer, err := newSealer("a-secret-long-enough-to-key-the-session-cipher")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sealed, err := sealer.seal("glpat-secret", "row-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(sealed, "glpat-secret") || !strings.HasPrefix(sealed, sealPrefix) {
		t.Fatalf("expected an opaque versioned ciphertext, got %q", sealed)
	}

	if opened, err := sealer.open(sealed, "row-a"); err != nil || opened != "glpat-secret" {
		t.Fatalf("expected the token back, got %q, %v", opened, err)
	}

	if _, err := sealer.open(sealed, "row-b"); !errors.Is(err, errUnsealable) {
		t.Errorf("expected a ciphertext moved to another row not to open, got %v", err)
	}

	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := sealer.open(tampered, "row-a"); !errors.Is(err, errUnsealable) {
		t.Errorf("expected a tampered ciphertext not to open, got %v", err)
	}

	other, _ := newSealer("a-different-secret-for-another-deployment")
	if _, err := other.open(sealed, "row-a"); !errors.Is(err, errUnsealable) {
		t.Errorf("expected a different secret not to open it, got %v", err)
	}

	if empty, err := sealer.seal("", "row-a"); err != nil || empty != "" {
		t.Errorf("expected an absent token to stay absent, got %q, %v", empty, err)
	}
}

func TestSessions_StoreNeitherTheCookieNorTheTokens(t *testing.T) {
	store := testSessions(t)

	id, _, err := store.create(t.Context(), sessionTokens{
		AccessToken:  "access-plaintext",
		RefreshToken: "refresh-plaintext",
		Expiry:       time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var rows []appdb.Session
	if err := store.conn.Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("expected one row, got %d (%v)", len(rows), err)
	}

	row := rows[0]
	if row.ID == id || row.ID != sessionKey(id) {
		t.Errorf("expected the row keyed by the cookie's hash, not the cookie")
	}

	for _, stored := range []string{row.AccessToken, row.RefreshToken} {
		if strings.Contains(stored, "plaintext") {
			t.Errorf("expected tokens encrypted at rest, found %q", stored)
		}
	}

	tokens, err := store.load(t.Context(), id)
	if err != nil || tokens.AccessToken != "access-plaintext" || tokens.RefreshToken != "refresh-plaintext" {
		t.Fatalf("expected the tokens back, got %+v (%v)", tokens, err)
	}
}

func TestSessions_LifetimeDependsOnRenewability(t *testing.T) {
	store := testSessions(t)
	tokenExpiry := time.Now().Add(2 * time.Hour)

	_, renewable, err := store.create(t.Context(), sessionTokens{AccessToken: "a", RefreshToken: "r", Expiry: tokenExpiry})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if time.Until(renewable) < DefaultSessionLifetime-time.Minute {
		t.Errorf("expected a renewable session to last its full lifetime, ends in %v", time.Until(renewable))
	}

	_, fixed, err := store.create(t.Context(), sessionTokens{AccessToken: "a", Expiry: tokenExpiry})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !fixed.Equal(tokenExpiry.UTC()) {
		t.Errorf("expected a session nothing can renew to end with its token, got %v want %v", fixed, tokenExpiry)
	}
}

func TestSessions_UnreadableRowsEndTheSession(t *testing.T) {
	store := testSessions(t)

	id, _, err := store.create(t.Context(), sessionTokens{AccessToken: "a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rotated, _ := newSealer("the-operator-rotated-the-session-secret-since")
	reader := &sessions{conn: store.conn, sealer: rotated, lifetime: DefaultSessionLifetime}

	if _, err := reader.load(t.Context(), id); !errors.Is(err, errNoSession) {
		t.Fatalf("expected a session sealed under an old secret to be gone, got %v", err)
	}

	var count int64
	store.conn.Model(&appdb.Session{}).Count(&count)

	if count != 0 {
		t.Errorf("expected the unreadable row to be deleted, %d left", count)
	}

	// A row from before tokens were sealed: raw identifier, plaintext token.
	legacy := appdb.Session{ID: sessionKey("legacy"), AccessToken: "plaintext", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.conn.Create(&legacy).Error; err != nil {
		t.Fatalf("failed to seed: %v", err)
	}

	if _, err := store.load(t.Context(), "legacy"); !errors.Is(err, errNoSession) {
		t.Errorf("expected a pre-encryption session to need a fresh login, got %v", err)
	}
}

// fakeRefresher stands in for GitLab's token endpoint.
type fakeRefresher struct {
	err   error
	calls atomic.Int32
	delay time.Duration
}

func (f *fakeRefresher) refresh(_ context.Context, refreshToken string) (sessionTokens, error) {
	f.calls.Add(1)
	time.Sleep(f.delay)

	if f.err != nil {
		return sessionTokens{}, f.err
	}

	return sessionTokens{
		AccessToken:  "renewed-from-" + refreshToken,
		RefreshToken: "rotated",
		Expiry:       time.Now().Add(2 * time.Hour),
	}, nil
}

func refreshingAPI(t *testing.T, refresher Refresher, verifier Verifier) (*API, string) {
	t.Helper()

	// A file rather than ":memory:", whose every pooled connection is a
	// separate empty database: the concurrency test needs several at once.
	conn, err := appdb.Open("sqlite://" + filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	if err := appdb.Migrate(conn); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	seedUser(t, conn, 42, "alice", 10)

	api := mustNew(conn, Options{Verifier: verifier})
	api.auth.refresher = refresher

	id, _, err := api.sessions.create(t.Context(), sessionTokens{
		AccessToken:  "stale",
		RefreshToken: "original",
		Expiry:       time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	return api, id
}

func withSession(id string) func(*http.Request) {
	return func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: id})
	}
}

func TestAuth_RenewsAnExpiredSessionTransparently(t *testing.T) {
	refresher := &fakeRefresher{}
	verifier := &recordingVerifier{identity: &gitlabclient.Identity{ID: 1}}
	api, id := refreshingAPI(t, refresher, verifier)

	rec := get(t, api, "/api/v1/users/alice/exp", withSession(id))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on a renewed session, got %d (%s)", rec.Code, rec.Body.String())
	}

	if verifier.lastToken != "renewed-from-original" {
		t.Errorf("expected the renewed token to be verified, got %q", verifier.lastToken)
	}

	tokens, err := api.sessions.load(t.Context(), id)
	if err != nil || tokens.RefreshToken != "rotated" || tokens.expiresWithin(time.Hour) {
		t.Errorf("expected the rotated tokens to be stored, got %+v (%v)", tokens, err)
	}

	get(t, api, "/api/v1/users/alice/exp", withSession(id))

	if refresher.calls.Load() != 1 {
		t.Errorf("expected a fresh token not to be renewed again, got %d renewals", refresher.calls.Load())
	}
}

// GitLab's refresh tokens work once, so concurrent requests on an expiring
// session must share one renewal rather than race and spend it twice.
func TestAuth_ConcurrentRequestsShareOneRenewal(t *testing.T) {
	refresher := &fakeRefresher{delay: 50 * time.Millisecond}
	api, id := refreshingAPI(t, refresher, &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	var (
		wg    sync.WaitGroup
		codes sync.Map
	)

	for index := range 10 {
		wg.Go(func() {
			codes.Store(index, get(t, api, "/api/v1/users/alice/exp", withSession(id)).Code)
		})
	}

	wg.Wait()

	codes.Range(func(_, code any) bool {
		if code != http.StatusOK {
			t.Errorf("expected every concurrent request to succeed, got %v", code)
		}

		return true
	})

	if refresher.calls.Load() != 1 {
		t.Fatalf("expected one renewal for ten concurrent requests, got %d", refresher.calls.Load())
	}
}

func TestAuth_RefusedRenewalEndsTheSession(t *testing.T) {
	refresher := &fakeRefresher{err: errRefreshRejected}
	api, id := refreshingAPI(t, refresher, &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	if rec := get(t, api, "/api/v1/users/alice/exp", withSession(id)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 once GitLab refuses the refresh token, got %d", rec.Code)
	}

	if _, err := api.sessions.load(t.Context(), id); !errors.Is(err, errNoSession) {
		t.Errorf("expected the dead session to be deleted, got %v", err)
	}
}

func TestAuth_UnreachableTokenEndpointIs503(t *testing.T) {
	refresher := &fakeRefresher{err: errors.New("connection refused")}
	api, id := refreshingAPI(t, refresher, &stubVerifier{identity: &gitlabclient.Identity{ID: 1}})

	if rec := get(t, api, "/api/v1/users/alice/exp", withSession(id)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when GitLab cannot be asked, got %d", rec.Code)
	}

	if _, err := api.sessions.load(t.Context(), id); err != nil {
		t.Errorf("expected the session to survive a transient failure, got %v", err)
	}
}
