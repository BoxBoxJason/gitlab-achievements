package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// SessionCookie is the cookie the OAuth flow puts a session identifier in.
//
// It carries the __Host- prefix, as do the flow's own cookies, so that only
// this app's origin can set it: a browser refuses a __Host- cookie that is
// not Secure, has a Domain, or a Path other than "/". Without it, a sibling
// subdomain could plant a parent-domain cookie under the same name, which
// req.Cookie would read first, and pin a visitor to the sibling's own
// session even after they log in. Behind a plain-http PUBLIC_URL nothing
// can be Secure, so the names lose the prefix there (see cookieName).
const SessionCookie = hostPrefix + "gitlab_achievements_session"

// hostPrefix is the cookie name prefix browsers bind to the exact origin
// that set it.
const hostPrefix = "__Host-"

// bearerPrefix is the Authorization scheme this app accepts. GitLab honors
// personal access tokens and OAuth access tokens alike on this header, so
// one scheme covers every kind of caller.
const bearerPrefix = "Bearer "

// Verifier resolves a GitLab credential to the identity behind it.
//
// It is an interface rather than *gitlabclient.TokenVerifier so that the
// handlers never depend on how verification happens: the group restriction
// (authorizingVerifier) and the short-lived identity cache
// (cachingVerifier) both wrap it here without any handler changing.
type Verifier interface {
	Verify(ctx context.Context, token string) (*gitlabclient.Identity, error)
}

// errNoCredential reports that the request carried nothing to authenticate
// with, as distinct from carrying something that turned out not to work.
var errNoCredential = errors.New("no credential presented")

const (
	// refreshMargin is how close to its expiry a session's access token is
	// renewed. Renewing a little early means a token never runs out between
	// being read here and being checked by GitLab.
	refreshMargin = time.Minute
	// refreshTimeout bounds one renewal, which runs detached from the
	// request that started it (see refresh).
	refreshTimeout = 15 * time.Second
)

// Refresher renews a session's GitLab tokens from its refresh token. It
// returns errRefreshRejected when GitLab will not renew them at all.
type Refresher interface {
	refresh(ctx context.Context, refreshToken string) (sessionTokens, error)
}

// errRefreshRejected reports GitLab refusing a refresh token: it was
// revoked, already used, or the application was deleted. The session it
// belongs to is over.
var errRefreshRejected = errors.New("gitlab refused to renew the session")

// identityKey types the request-context key holding the caller's identity,
// so it cannot collide with a key set anywhere else.
type identityKey struct{}

// IdentityFrom returns the GitLab identity the request was authenticated
// as, if any. It is absent when authentication is disabled.
func IdentityFrom(ctx context.Context) (*gitlabclient.Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(*gitlabclient.Identity)

	return identity, ok
}

// authenticator turns a request's credential into a GitLab identity.
//
// Every authenticated caller may read everything the API serves. That is
// deliberate: achievements are already public on GitLab profiles and are
// social by nature, so the thing worth preventing is an anonymous caller
// enumerating who exists on the instance, which authentication alone
// settles. There is consequently no per-endpoint authorization below.
type authenticator struct {
	verifier  Verifier
	sessions  *sessions
	refresher Refresher
	renewals  singleflight.Group
	// sessionCookie is the name SessionCookie goes by on this deployment.
	sessionCookie string
}

// middleware wraps next so that it is only reached by requests carrying a
// credential GitLab recognizes.
//
// The two failure modes are answered differently on purpose. A credential
// GitLab rejects is the caller's problem and is 401. A credential that
// could not be checked at all, because the instance is unreachable, is this
// app's problem and is 503: answering 401 there would tell a caller their
// token is bad when it is fine, and invite them to go and rotate it.
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		token, err := a.credential(req)
		if err != nil {
			a.rejectCredential(resp, err)

			return
		}

		identity, err := a.verifier.Verify(req.Context(), token)
		if err != nil {
			a.reject(resp, err)

			return
		}

		next.ServeHTTP(resp, req.WithContext(context.WithValue(req.Context(), identityKey{}, identity)))
	})
}

// reject answers a failed verification, keeping GitLab's own error out of
// the response body and in the log instead: it can name internal hosts, and
// a caller can do nothing with it either way.
func (a *authenticator) reject(resp http.ResponseWriter, err error) {
	if errors.Is(err, gitlabclient.ErrInvalidToken) {
		writeError(resp, http.StatusUnauthorized, "invalid credential")

		return
	}

	if errors.Is(err, ErrForbidden) {
		writeError(resp, http.StatusForbidden, "this GitLab account is not allowed to use this api")

		return
	}

	zap.L().Warn("failed to verify an api credential against gitlab", zap.Error(err))
	writeError(resp, http.StatusServiceUnavailable, "cannot verify credentials right now")
}

// rejectCredential answers a request whose credential could not even be
// assembled.
//
// A missing, malformed, or dead-session credential is the caller's problem
// and is 401. A session that could not be looked up because the database
// failed is this app's problem, and gets the same 503 an unreachable GitLab
// does: the caller's cookie may well be perfectly good.
func (a *authenticator) rejectCredential(resp http.ResponseWriter, err error) {
	if errors.Is(err, errNoCredential) || errors.Is(err, errNoSession) {
		writeError(resp, http.StatusUnauthorized, "authentication required")

		return
	}

	zap.L().Warn("failed to resolve an api credential", zap.Error(err))
	writeError(resp, http.StatusServiceUnavailable, "cannot verify credentials right now")
}

// credential pulls the caller's GitLab token out of the request: an
// Authorization header for programmatic callers, or the session cookie the
// OAuth flow set for browsers.
//
// The header is preferred, so a caller who sends one explicitly is never
// silently authenticated as whoever the browser happens to be logged in as.
func (a *authenticator) credential(req *http.Request) (string, error) {
	header := req.Header.Get("Authorization")
	if header != "" {
		token, ok := strings.CutPrefix(header, bearerPrefix)
		if !ok || strings.TrimSpace(token) == "" {
			return "", fmt.Errorf("%w: malformed Authorization header", errNoCredential)
		}

		return token, nil
	}

	cookie, err := req.Cookie(a.sessionCookie)
	if err != nil {
		return "", errNoCredential
	}

	return a.sessionToken(req.Context(), cookie.Value)
}

// sessionToken returns the access token the session behind a cookie holds,
// renewing it first if it is about to run out.
func (a *authenticator) sessionToken(ctx context.Context, sessionID string) (string, error) {
	tokens, err := a.sessions.load(ctx, sessionID)
	if err != nil {
		return "", err
	}

	if a.refresher != nil && tokens.RefreshToken != "" && tokens.expiresWithin(refreshMargin) {
		renewed, refreshErr := a.refresh(ctx, sessionID)

		switch {
		case refreshErr == nil:
			tokens = renewed
		case errors.Is(refreshErr, errNoSession) || !time.Now().Before(tokens.Expiry):
			return "", refreshErr
		default:
			// Renewal starts a little early, so a token GitLab could not
			// renew just now usually still works: use it rather than
			// answering 503 for a credential that is fine, and try again
			// on the next request.
			zap.L().Warn("failed to renew a session, using its current token meanwhile", zap.Error(refreshErr))
		}
	}

	return tokens.AccessToken, nil
}

// refresh renews a session's tokens, once however many requests ask at
// the same moment.
//
// GitLab rotates refresh tokens: each one works once. A page firing several
// requests just as its token runs out would otherwise race to renew it,
// and every request but the first would present a refresh token that had
// just been spent, and lose the session. The renewal is shared instead,
// and runs on a context of its own so the first request being abandoned
// does not fail it for the others waiting on it.
//
// The session is read again inside, because a renewal that finished just
// before this one started has already done the work.
func (a *authenticator) refresh(ctx context.Context, sessionID string) (sessionTokens, error) {
	renewed, err, _ := a.renewals.Do(sessionKey(sessionID), func() (any, error) {
		renewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()

		current, err := a.sessions.load(renewCtx, sessionID)
		if err != nil || !current.expiresWithin(refreshMargin) {
			return current, err
		}

		tokens, err := a.refresher.refresh(renewCtx, current.RefreshToken)
		if errors.Is(err, errRefreshRejected) {
			zap.L().Info("gitlab refused to renew a session, ending it", zap.Error(err))

			_ = a.sessions.destroy(renewCtx, sessionID)

			return sessionTokens{}, errNoSession
		}

		if err != nil {
			return sessionTokens{}, fmt.Errorf("failed to renew session: %w", err)
		}

		// The renewal worked even if recording it did not: the session
		// store holds the new tokens until it can write them (see
		// sessions.update), so the request goes ahead on them.
		err = a.sessions.update(renewCtx, sessionID, tokens)
		if err != nil {
			zap.L().Warn("renewed a session but failed to save its tokens, holding them until it can", zap.Error(err))
		}

		return tokens, nil
	})
	if err != nil {
		return sessionTokens{}, err //nolint:wrapcheck // wrapped inside the shared call
	}

	// The shared call only ever returns sessionTokens.
	tokens, _ := renewed.(sessionTokens)

	return tokens, nil
}
