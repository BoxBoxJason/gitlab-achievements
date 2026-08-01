package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// SessionCookie is the cookie the OAuth flow puts a session identifier in.
const SessionCookie = "gitlab_achievements_session"

// bearerPrefix is the Authorization scheme this app accepts. GitLab honors
// personal access tokens and OAuth access tokens alike on this header, so
// one scheme covers every kind of caller.
const bearerPrefix = "Bearer "

// Verifier resolves a GitLab credential to the identity behind it.
//
// It is an interface rather than *gitlabclient.TokenVerifier so that the
// handlers never depend on how verification happens. Verification currently
// calls GitLab on every request by design, and this is the seam a
// short-lived identity cache would slot into without any handler changing.
type Verifier interface {
	Verify(ctx context.Context, token string) (*gitlabclient.Identity, error)
}

// errNoCredential reports that the request carried nothing to authenticate
// with, as distinct from carrying something that turned out not to work.
var errNoCredential = errors.New("no credential presented")

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
	verifier Verifier
	sessions *sessions
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

	cookie, err := req.Cookie(SessionCookie)
	if err != nil {
		return "", errNoCredential
	}

	token, err := a.sessions.token(req.Context(), cookie.Value)
	if err != nil {
		return "", err
	}

	return token, nil
}
