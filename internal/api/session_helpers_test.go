package api

import (
	"context"
	"crypto/sha256"
	"testing"
)

// testSessions builds a session store over a fresh database, with a fixed
// key so a test can build a second store that reads the first's rows.
func testSessions(t *testing.T) *sessions {
	t.Helper()

	store, err := newSessionStore(testConn(t), Options{SessionSecret: "test-secret-that-is-long-enough-for-hkdf"})
	if err != nil {
		t.Fatalf("failed to build session store: %v", err)
	}

	return store
}

// loadToken returns only the access token a session holds.
func (s *sessions) loadToken(ctx context.Context, sessionID string) (string, error) {
	tokens, err := s.load(ctx, sessionID)

	return tokens.AccessToken, err
}

func sha256Of(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}
