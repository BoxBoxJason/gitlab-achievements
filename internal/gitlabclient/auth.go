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

	"github.com/boxboxjason/gitlab-achievements/internal/metrics"
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
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	ID        int64  `json:"id"`
	IsAdmin   bool   `json:"is_admin"`
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
// A verifier accepts a personal access token, or an OAuth access token
// issued by the application this app registers. GitLab itself honors an
// OAuth token from any application on /api/v4/user, so that alone would
// let any other relying party on the instance (a "Sign in with GitLab"
// service, or an application some user created and the victim authorized)
// replay its users' tokens here and act as them. The issuing application is
// therefore checked first, through /oauth/token/info.
type TokenVerifier struct {
	http     *http.Client
	baseURL  string
	clientID string
}

// NewTokenVerifier builds a TokenVerifier for the instance at baseURL,
// accepting OAuth access tokens only when the application clientID names
// issued them.
//
// It holds one http.Client for the life of the process rather than building
// one per request, so verification reuses connections instead of paying a
// new TLS handshake on every authenticated API call.
func NewTokenVerifier(baseURL, clientID string) (*TokenVerifier, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, errors.New("failed to build gitlab token verifier: base URL is empty")
	}

	// Without a client ID every OAuth token would be some other
	// application's; refusing to build is clearer than refusing them all.
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("failed to build gitlab token verifier: oauth client ID is empty")
	}

	return &TokenVerifier{
		baseURL:  trimmed,
		clientID: clientID,
		http: &http.Client{
			Timeout:   verifyTimeout,
			Transport: metrics.Transport("verify", http.DefaultTransport),
		},
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

	err := v.checkApplication(ctx, token)
	if err != nil {
		return nil, err
	}

	resp, err := v.get(ctx, "/api/v4/user", token)
	if err != nil {
		return nil, fmt.Errorf("failed to verify token against gitlab: %w", err)
	}

	defer resp.Body.Close() //nolint:errcheck // nothing actionable on a response body close

	return decodeIdentity(resp)
}

// checkApplication refuses an OAuth access token issued to any application
// but this app's.
//
// GitLab's token info endpoint only knows OAuth access tokens: it answers
// 401 for anything else, a personal access token included, and those go on
// to be checked as usual. (A dead OAuth token gets the same 401 here, and
// /api/v4/user refuses it next.)
func (v *TokenVerifier) checkApplication(ctx context.Context, token string) error {
	resp, err := v.get(ctx, "/oauth/token/info", token)
	if err != nil {
		return fmt.Errorf("failed to look up the token's oauth application on gitlab: %w", err)
	}

	defer resp.Body.Close() //nolint:errcheck // nothing actionable on a response body close

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("gitlab returned status %d looking up a token's oauth application", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIdentityBytes))
	if err != nil {
		return fmt.Errorf("failed to read gitlab token info response: %w", err)
	}

	var info struct {
		Application struct {
			UID string `json:"uid"`
		} `json:"application"`
	}

	err = json.Unmarshal(body, &info)
	if err != nil {
		return fmt.Errorf("failed to decode gitlab token info response: %w", err)
	}

	if info.Application.UID != v.clientID {
		return fmt.Errorf("%w: it was issued to another oauth application", ErrInvalidToken)
	}

	return nil
}

// get sends an authenticated GET for path with token as the bearer.
func (v *TokenVerifier) get(ctx context.Context, path, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build gitlab request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err //nolint:wrapcheck // callers say which lookup failed
	}

	return resp, nil
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
