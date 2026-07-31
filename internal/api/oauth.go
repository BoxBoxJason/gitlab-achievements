package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

const (
	// LoginPath starts the login flow.
	LoginPath = OAuthPathPrefix + "login"
	// CallbackPath is where GitLab returns the authorization code. It is
	// what the registered application's redirect URI must point at.
	CallbackPath = OAuthPathPrefix + "callback"
	// LogoutPath ends a session.
	LogoutPath = OAuthPathPrefix + "logout"

	// stateCookie carries the CSRF nonce across the redirect to GitLab, to
	// be compared with the state GitLab hands back.
	stateCookie = "gitlab_achievements_oauth_state"
	// verifierCookie carries the PKCE verifier across the same redirect.
	// Only the browser that started the flow holds it, which is what stops
	// an intercepted authorization code from being exchanged by anyone else.
	verifierCookie = "gitlab_achievements_oauth_verifier"

	// flowTTL bounds how long a login may sit half-finished. It only has to
	// outlast a human deciding whether to authorize the application.
	flowTTL = 10 * time.Minute
	// stateBytes is the entropy in the CSRF nonce.
	stateBytes = 32
)

// OAuthOptions configures the login flow.
type OAuthOptions struct {
	// GitLabURL is the instance that authorizes logins.
	GitLabURL string
	// PublicURL is this app's externally reachable base URL, which the
	// redirect URI is built from. It must match what the OAuth application
	// on GitLab was registered with, or GitLab refuses the authorization.
	PublicURL string
	// ClientID identifies the OAuth application. ClientSecret is optional:
	// with one the app is a confidential client, without one a public
	// client relying on PKCE alone.
	ClientID     string
	ClientSecret string
}

// oauthFlow implements the authorization-code flow against GitLab.
//
// PKCE is used whether or not there is a client secret. For the public
// client it is what secures the exchange at all; for the confidential one
// it costs nothing and closes code interception regardless.
type oauthFlow struct {
	config   *oauth2.Config
	sessions *sessions
	verifier Verifier
	logger   *zap.Logger
	secure   bool
}

// newOAuthFlow builds the flow.
//
// secure is derived from PublicURL's scheme rather than configured
// separately: the cookies below are only marked Secure when this app is
// actually reachable over https, because a Secure cookie is never sent back
// over a plain-http deployment and the flow would silently never complete.
func newOAuthFlow(opts OAuthOptions, sessionStore *sessions, verifier Verifier, logger *zap.Logger) *oauthFlow {
	base := strings.TrimRight(opts.GitLabURL, "/")

	return &oauthFlow{
		config: &oauth2.Config{
			ClientID:     opts.ClientID,
			ClientSecret: opts.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  base + "/oauth/authorize",
				TokenURL: base + "/oauth/token",
			},
			RedirectURL: strings.TrimRight(opts.PublicURL, "/") + CallbackPath,
			Scopes:      []string{"read_user"},
		},
		sessions: sessionStore,
		verifier: verifier,
		logger:   logger,
		secure:   strings.HasPrefix(strings.ToLower(opts.PublicURL), "https://"),
	}
}

// routes registers the login flow. None of these sit behind authentication:
// they are how a browser acquires a credential.
func (f *oauthFlow) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+LoginPath, f.handleLogin)
	mux.HandleFunc("GET "+CallbackPath, f.handleCallback)
	mux.HandleFunc("POST "+LogoutPath, f.handleLogout)
}

// handleLogin sends the browser to GitLab to authorize this application,
// having first stashed the CSRF nonce and PKCE verifier it will need to
// finish the exchange when GitLab sends it back.
func (f *oauthFlow) handleLogin(resp http.ResponseWriter, req *http.Request) {
	state, err := randomToken()
	if err != nil {
		f.logger.Error("failed to start an oauth login", zap.Error(err))
		writeError(resp, http.StatusInternalServerError, "internal error")

		return
	}

	verifier := oauth2.GenerateVerifier()

	f.setFlowCookie(resp, stateCookie, state)
	f.setFlowCookie(resp, verifierCookie, verifier)

	http.Redirect(resp, req,
		f.config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)),
		http.StatusFound)
}

// handleCallback finishes the flow: it checks the request really is the one
// this browser started, exchanges the authorization code for a token, and
// opens a session on it.
func (f *oauthFlow) handleCallback(resp http.ResponseWriter, req *http.Request) {
	// GitLab reports a refusal (the user pressed Deny, the application is
	// misconfigured) in the query string rather than by status, so this is
	// checked before anything else is trusted.
	if gitlabErr := req.URL.Query().Get("error"); gitlabErr != "" {
		f.clearFlowCookies(resp)
		f.logger.Info("gitlab refused an oauth login", zap.String("error", gitlabErr))
		writeError(resp, http.StatusUnauthorized, "gitlab refused the login")

		return
	}

	code, err := f.validateCallback(req)
	if err != nil {
		f.clearFlowCookies(resp)
		f.logger.Warn("rejected an oauth callback", zap.Error(err))
		writeError(resp, http.StatusBadRequest, "invalid login callback")

		return
	}

	verifier, err := req.Cookie(verifierCookie)
	if err != nil {
		f.clearFlowCookies(resp)
		writeError(resp, http.StatusBadRequest, "login expired, start again")

		return
	}

	token, err := f.config.Exchange(req.Context(), code, oauth2.VerifierOption(verifier.Value))
	if err != nil {
		f.clearFlowCookies(resp)
		f.logger.Warn("failed to exchange an oauth authorization code", zap.Error(err))
		writeError(resp, http.StatusBadGateway, "could not complete the login with gitlab")

		return
	}

	f.establish(resp, req, token)
}

// validateCallback checks the callback carries a code and a state matching
// the one this browser was sent away with.
//
// The comparison is constant-time for the same reason the webhook token's
// is: a nonce that can be discovered a byte at a time by measuring the
// response is not a nonce.
func (f *oauthFlow) validateCallback(req *http.Request) (string, error) {
	state, err := req.Cookie(stateCookie)
	if err != nil {
		return "", errors.New("no login in progress for this browser")
	}

	returned := req.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(returned), []byte(state.Value)) != 1 {
		return "", errors.New("state did not match the one this login started with")
	}

	code := req.URL.Query().Get("code")
	if code == "" {
		return "", errors.New("callback carried no authorization code")
	}

	return code, nil
}

// establish opens a session on a freshly issued token and hands the browser
// its cookie.
//
// The token is verified once here before any session exists, so a login
// that produced a credential GitLab will not honor fails at the login
// rather than on the next request. It also yields the username, which is
// what the browser is sent to: with no UI of its own, the most useful place
// to land somebody who just logged in is their own record.
func (f *oauthFlow) establish(resp http.ResponseWriter, req *http.Request, token *oauth2.Token) {
	identity, err := f.verifier.Verify(req.Context(), token.AccessToken)
	if err != nil {
		f.clearFlowCookies(resp)
		f.logger.Warn("gitlab issued a token it would not then honor", zap.Error(err))
		writeError(resp, http.StatusBadGateway, "could not complete the login with gitlab")

		return
	}

	sessionID, err := f.sessions.create(req.Context(), token.AccessToken, token.Expiry)
	if err != nil {
		f.clearFlowCookies(resp)
		f.logger.Error("failed to open a session", zap.Error(err))
		writeError(resp, http.StatusInternalServerError, "internal error")

		return
	}

	f.clearFlowCookies(resp)
	http.SetCookie(resp, f.cookie(SessionCookie, sessionID, sessionCookieAge(token.Expiry)))

	http.Redirect(resp, req, PathPrefix+"users/"+url.PathEscape(identity.Username), http.StatusFound)
}

// handleLogout ends the session the cookie names and clears it.
//
// It is a POST because it changes state, which keeps a link or a prefetch
// from logging somebody out.
func (f *oauthFlow) handleLogout(resp http.ResponseWriter, req *http.Request) {
	cookie, err := req.Cookie(SessionCookie)
	if err == nil {
		destroyErr := f.sessions.destroy(req.Context(), cookie.Value)
		if destroyErr != nil {
			f.logger.Warn("failed to delete a session on logout", zap.Error(destroyErr))
		}
	}

	// Cleared regardless: a session row that could not be deleted is worse
	// left paired with a cookie that still points at it.
	http.SetCookie(resp, f.cookie(SessionCookie, "", -1))
	resp.WriteHeader(http.StatusNoContent)
}

// setFlowCookie stores one short-lived value for the duration of a login.
func (f *oauthFlow) setFlowCookie(resp http.ResponseWriter, name, value string) {
	cookie := f.cookie(name, value, int(flowTTL.Seconds())) //nolint:gosec // attributes are set by f.cookie
	cookie.Path = OAuthPathPrefix

	http.SetCookie(resp, cookie)
}

// clearFlowCookies drops the login's temporary state, whether it completed
// or not, so a failed attempt cannot be replayed and a stale verifier
// cannot be paired with a later code.
func (f *oauthFlow) clearFlowCookies(resp http.ResponseWriter) {
	for _, name := range []string{stateCookie, verifierCookie} {
		cookie := f.cookie(name, "", -1) //nolint:gosec // attributes are set by f.cookie
		cookie.Path = OAuthPathPrefix

		http.SetCookie(resp, cookie)
	}
}

// cookie builds one of this flow's cookies.
//
// All of them are HttpOnly, since nothing here is meant to be read by
// scripts, and SameSite=Lax, which still permits the top-level redirect
// back from GitLab while keeping the session cookie off cross-site
// requests.
func (f *oauthFlow) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // HttpOnly/Secure/SameSite are all set below
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   f.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// sessionCookieAge matches the cookie's lifetime to the token's, falling
// back to the session store's own default when GitLab supplied no expiry.
func sessionCookieAge(expiry time.Time) int {
	if expiry.IsZero() {
		return int(defaultSessionTTL.Seconds())
	}

	remaining := time.Until(expiry)
	if remaining <= 0 {
		return int(defaultSessionTTL.Seconds())
	}

	return int(remaining.Seconds())
}

// randomToken returns a URL-safe random value for use as a CSRF nonce.
func randomToken() (string, error) {
	raw := make([]byte, stateBytes)

	_, err := rand.Read(raw)
	if err != nil {
		return "", err //nolint:wrapcheck // wrapped by the caller with the operation that needed it
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}
