package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

const (
	// sessionIDBytes is how much entropy a session identifier carries. The
	// identifier is the whole of what the session cookie holds, so guessing
	// one is impersonating its owner; 256 bits puts that out of reach.
	sessionIDBytes = 32
	// defaultSessionTTL bounds a session GitLab issued a token for without
	// saying when it expires. GitLab access tokens are short-lived and come
	// with an expiry, so this is a backstop rather than the usual case.
	defaultSessionTTL = 24 * time.Hour
	// pruneInterval is how often expired sessions are swept from the table.
	pruneInterval = time.Hour
)

// errNoSession reports that a session cookie referred to no live session:
// it expired, it was logged out, or it was never real.
var errNoSession = errors.New("no such session")

// sessions stores the browser sessions the OAuth callback establishes.
//
// A session holds the GitLab access token it was created from, because
// every request re-verifies against GitLab and the token is the only thing
// GitLab will answer questions about. The cookie carries the session's
// identifier and never the token itself, so the credential is not handed
// back to the browser on each response.
type sessions struct {
	conn *gorm.DB
}

// create records a new session for token and returns its identifier, which
// is what the caller puts in the cookie.
//
// expiry is GitLab's own expiry for the token where it supplied one, so a
// session cannot outlive the credential it depends on; a zero or past
// expiry falls back to defaultSessionTTL.
func (s *sessions) create(ctx context.Context, token string, expiry time.Time) (string, error) {
	raw := make([]byte, sessionIDBytes)

	_, err := rand.Read(raw)
	if err != nil {
		return "", fmt.Errorf("failed to generate a session identifier: %w", err)
	}

	sessionID := base64.RawURLEncoding.EncodeToString(raw)

	if expiry.IsZero() || !expiry.After(time.Now()) {
		expiry = time.Now().Add(defaultSessionTTL)
	}

	err = s.conn.WithContext(ctx).Create(&db.Session{
		ID:          sessionID,
		AccessToken: token,
		ExpiresAt:   expiry.UTC(),
	}).Error
	if err != nil {
		return "", fmt.Errorf("failed to persist session: %w", err)
	}

	return sessionID, nil
}

// token returns the GitLab credential a live session stands for, reporting
// errNoSession when the identifier matches nothing still valid.
//
// Expiry is applied in the query rather than checked afterwards, so a
// session that has aged out is indistinguishable from one that never
// existed even if pruning has not yet run.
func (s *sessions) token(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" {
		return "", errNoSession
	}

	var session db.Session

	err := s.conn.WithContext(ctx).
		Where("id = ? AND expires_at > ?", sessionID, time.Now().UTC()).
		First(&session).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", errNoSession
	}

	if err != nil {
		return "", fmt.Errorf("failed to load session: %w", err)
	}

	return session.AccessToken, nil
}

// destroy ends one session. Deleting an identifier that is already gone is
// not an error: logging out twice, or logging out of a session that expired
// in between, should both simply leave the caller logged out.
func (s *sessions) destroy(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	err := s.conn.WithContext(ctx).Where("id = ?", sessionID).Delete(&db.Session{}).Error
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	return nil
}

// prune deletes sessions that have expired, returning how many went.
//
// Expired sessions are already unusable, so this is housekeeping rather
// than enforcement: without it the table would grow for the life of the
// deployment, one row per login that was never logged out of.
func (s *sessions) prune(ctx context.Context) (int64, error) {
	result := s.conn.WithContext(ctx).
		Where("expires_at <= ?", time.Now().UTC()).
		Delete(&db.Session{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to prune expired sessions: %w", result.Error)
	}

	return result.RowsAffected, nil
}
