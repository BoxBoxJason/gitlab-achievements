package gitlabclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrInvalidToken reports that GitLab rejected the presented credential:
// it is missing, malformed, expired, revoked, or lacks the scope to read
// the identity behind it.
//
// It is distinguished from every other failure because the two mean
// opposite things to a caller. A rejected token will be rejected again, and
// belongs to whoever sent it; anything else means this app could not reach
// GitLab to ask, which is this app's problem and is worth retrying.
var ErrInvalidToken = errors.New("gitlab rejected the token")

// verifyTimeout bounds a single identity lookup.
//
// It is deliberately short. This call sits in front of every authenticated
// API request, so an instance that has become slow rather than unreachable
// would otherwise hold request goroutines open for as long as it takes to
// answer, turning a degraded GitLab into an exhausted API server.
const verifyTimeout = 10 * time.Second

// Identity is who a token belongs to on the GitLab instance.
type Identity struct {
	Username string `json:"username"`
	ID       int64  `json:"id"`
	IsAdmin  bool   `json:"is_admin"`
}

// TokenVerifier resolves arbitrary GitLab credentials to the identity
// behind them.
//
// It exists apart from ReadClient and WriteClient because it is a different
// kind of thing: those two wrap one fixed token belonging to this app,
// while this one is handed a different caller's token on every call and is
// never authorized to do anything on its own. Keeping it separate means no
// code path can accidentally use a visitor's credential to act on the
// instance.
//
// A verifier accepts any token type GitLab honors on the Authorization
// header: a personal access token, or an OAuth access token issued by the
// application this app registers.
type TokenVerifier struct {
	http    *http.Client
	baseURL string
}

// NewTokenVerifier builds a TokenVerifier for the instance at baseURL.
//
// It holds one http.Client for the life of the process rather than building
// one per request, so verification reuses connections instead of paying a
// new TLS handshake on every authenticated API call.
func NewTokenVerifier(baseURL string) (*TokenVerifier, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, errors.New("failed to build gitlab token verifier: base URL is empty")
	}

	return &TokenVerifier{
		baseURL: trimmed,
		http:    &http.Client{Timeout: verifyTimeout},
	}, nil
}

// Verify resolves token to the identity GitLab says it belongs to.
//
// It deliberately makes no attempt to cache: a credential is re-checked on
// every call, so a token revoked on GitLab stops working here immediately
// rather than at the end of some cache window. The cost is that an
// authenticated request cannot be served while the instance is unreachable.
func (v *TokenVerifier) Verify(ctx context.Context, token string) (*Identity, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: no credential presented", ErrInvalidToken)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.baseURL+"/api/v4/user", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build gitlab identity request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to verify token against gitlab: %w", err)
	}

	defer resp.Body.Close() //nolint:errcheck // nothing actionable on a response body close

	return decodeIdentity(resp)
}

// decodeIdentity turns GitLab's answer into an Identity, mapping a refusal
// to ErrInvalidToken and everything else to a plain error.
func decodeIdentity(resp *http.Response) (*Identity, error) {
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (status %d)", ErrInvalidToken, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("gitlab returned status %d verifying a token", resp.StatusCode)
	}

	// Bounded because the body is attacker-influenced only in so far as it
	// comes from whatever host baseURL points at, but an unbounded decode
	// from a misconfigured URL is an easy way to lose the process.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIdentityBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read gitlab identity response: %w", err)
	}

	var identity Identity

	err = json.Unmarshal(body, &identity)
	if err != nil {
		return nil, fmt.Errorf("failed to decode gitlab identity response: %w", err)
	}

	// A 200 carrying no user ID is not an identity, whatever else it is;
	// treating it as one would authenticate a caller as user zero.
	if identity.ID == 0 {
		return nil, fmt.Errorf("%w: gitlab returned no user for it", ErrInvalidToken)
	}

	return &identity, nil
}

// maxIdentityBytes bounds how much of GitLab's identity response is read.
// The document is a single user object; this is orders of magnitude above
// what one can be.
const maxIdentityBytes = 1 << 20 // 1 MiB
