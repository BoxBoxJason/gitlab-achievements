package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	// DefaultSessionLifetime is how long a browser stays signed in when no
	// lifetime is configured. A session renews its GitLab token as it runs
	// out, so this, not the token's two hours, is what decides how often
	// somebody has to log in again.
	DefaultSessionLifetime = 7 * 24 * time.Hour
	// pruneInterval is how often expired sessions are swept from the table.
	pruneInterval = time.Hour
)

// errNoSession reports that a session cookie referred to no live session:
// it expired, it was logged out, it was never real, or its stored tokens
// can no longer be decrypted.
var errNoSession = errors.New("no such session")

// sessionTokens are the GitLab credentials a session stands for.
type sessionTokens struct {
	// Expiry is when the access token runs out, zero when GitLab did not
	// say.
	Expiry       time.Time
	AccessToken  string
	RefreshToken string
}

// expiresWithin reports whether the access token runs out within margin.
// A token with no known expiry never does, as far as this app can tell.
func (t sessionTokens) expiresWithin(margin time.Duration) bool {
	return !t.Expiry.IsZero() && time.Until(t.Expiry) < margin
}

// sessions stores the browser sessions the OAuth callback establishes.
//
// A session holds the GitLab tokens it was created from, because every
// request is verified against GitLab and the token is the only thing GitLab
// will answer questions about. The cookie carries the session's identifier
// and never a token, so no credential is handed back to the browser; the
// table holds only a hash of that identifier and encrypted tokens, so a
// copy of it is not a way in either.
type sessions struct {
	conn     *gorm.DB
	sealer   *sealer
	lifetime time.Duration
}

// sessionKey is the row key for the identifier a cookie carries.
func sessionKey(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))

	return hex.EncodeToString(sum[:])
}

// create records a new session for tokens and returns the identifier the
// cookie should carry and when the session ends.
//
// A session with a refresh token lasts its full lifetime, renewing the
// access token as it goes. One without can last no longer than its access
// token, since nothing could renew it.
func (s *sessions) create(ctx context.Context, tokens sessionTokens) (string, time.Time, error) {
	raw := make([]byte, sessionIDBytes)

	_, err := rand.Read(raw)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to generate a session identifier: %w", err)
	}

	sessionID := base64.RawURLEncoding.EncodeToString(raw)
	key := sessionKey(sessionID)
	expiresAt := time.Now().Add(s.lifetime).UTC()

	if tokens.RefreshToken == "" && !tokens.Expiry.IsZero() && tokens.Expiry.Before(expiresAt) {
		expiresAt = tokens.Expiry.UTC()
	}

	row := db.Session{ID: key, ExpiresAt: expiresAt}

	err = s.sealInto(&row, tokens)
	if err != nil {
		return "", time.Time{}, err
	}

	err = s.conn.WithContext(ctx).Create(&row).Error
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to persist session: %w", err)
	}

	return sessionID, expiresAt, nil
}

// load returns the tokens a live session stands for, reporting
// errNoSession when the identifier matches nothing still valid.
//
// Expiry is applied in the query rather than checked afterwards, so a
// session that has aged out is indistinguishable from one that never
// existed even if pruning has not yet run. A row whose tokens will not
// decrypt (sealed under a secret since rotated, or written before tokens
// were sealed) is deleted and reported the same way: it is unusable, and
// the browser holding it needs to log in again.
func (s *sessions) load(ctx context.Context, sessionID string) (sessionTokens, error) {
	if sessionID == "" {
		return sessionTokens{}, errNoSession
	}

	key := sessionKey(sessionID)

	var row db.Session

	err := s.conn.WithContext(ctx).
		Where("id = ? AND expires_at > ?", key, time.Now().UTC()).
		First(&row).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sessionTokens{}, errNoSession
	}

	if err != nil {
		return sessionTokens{}, fmt.Errorf("failed to load session: %w", err)
	}

	tokens, err := s.unseal(&row)
	if errors.Is(err, errUnsealable) {
		_ = s.destroy(ctx, sessionID)

		return sessionTokens{}, errNoSession
	}

	return tokens, err
}

// update replaces the tokens a session holds, after a refresh. The session
// keeps the end it was created with.
func (s *sessions) update(ctx context.Context, sessionID string, tokens sessionTokens) error {
	row := db.Session{ID: sessionKey(sessionID)}

	err := s.sealInto(&row, tokens)
	if err != nil {
		return err
	}

	err = s.conn.WithContext(ctx).Model(&db.Session{}).Where("id = ?", row.ID).Updates(map[string]any{
		"access_token":     row.AccessToken,
		"refresh_token":    row.RefreshToken,
		"token_expires_at": row.TokenExpiresAt,
	}).Error
	if err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	return nil
}

// destroy ends one session. Deleting an identifier that is already gone is
// not an error: logging out twice, or logging out of a session that expired
// in between, should both simply leave the caller logged out.
func (s *sessions) destroy(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	err := s.conn.WithContext(ctx).Where("id = ?", sessionKey(sessionID)).Delete(&db.Session{}).Error
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

// sealInto encrypts tokens onto row, bound to row.ID.
func (s *sessions) sealInto(row *db.Session, tokens sessionTokens) error {
	access, err := s.sealer.seal(tokens.AccessToken, row.ID)
	if err != nil {
		return fmt.Errorf("failed to encrypt session token: %w", err)
	}

	refresh, err := s.sealer.seal(tokens.RefreshToken, row.ID)
	if err != nil {
		return fmt.Errorf("failed to encrypt session token: %w", err)
	}

	row.AccessToken = access
	row.RefreshToken = refresh
	row.TokenExpiresAt = nil

	if !tokens.Expiry.IsZero() {
		expiry := tokens.Expiry.UTC()
		row.TokenExpiresAt = &expiry
	}

	return nil
}

// unseal decrypts the tokens on row.
func (s *sessions) unseal(row *db.Session) (sessionTokens, error) {
	access, err := s.sealer.open(row.AccessToken, row.ID)
	if err != nil {
		return sessionTokens{}, err
	}

	refresh, err := s.sealer.open(row.RefreshToken, row.ID)
	if err != nil {
		return sessionTokens{}, err
	}

	tokens := sessionTokens{AccessToken: access, RefreshToken: refresh}
	if row.TokenExpiresAt != nil {
		tokens.Expiry = *row.TokenExpiresAt
	}

	return tokens, nil
}
