package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/boxboxjason/gitlab-achievements/internal/metrics"
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
	// returnCookie carries where to send the browser once it is logged in.
	returnCookie = "gitlab_achievements_oauth_return"

	// returnToParam is the login query parameter naming where to land.
	returnToParam = "return_to"

	// flowTTL bounds how long a login may sit half-finished. It only has to
	// outlast a human deciding whether to authorize the application.
	flowTTL = 10 * time.Minute
	// stateBytes is the entropy in the CSRF nonce.
	stateBytes = 32
	// tokenEndpointTimeout bounds one call to GitLab's token endpoint, for
	// the same reason the identity lookup is bounded: a slow instance must
	// not hold requests open for as long as it takes to answer.
	tokenEndpointTimeout = 10 * time.Second
)

// errReturnTo reports a return_to the login flow will not send a browser
// to.
var errReturnTo = errors.New("return_to must be a path on this app or a URL on an allowed origin")

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
	// ReturnOrigins are the origins, besides PublicURL's own, a browser may
	// be sent back to after logging in, e.g. https://rankings.example.com.
	ReturnOrigins []string
}

// oauthFlow implements the authorization-code flow against GitLab.
//
// PKCE is used whether or not there is a client secret. For the public
// client it is what secures the exchange at all; for the confidential one
// it costs nothing and closes code interception regardless.
type oauthFlow struct {
	config        *oauth2.Config
	sessions      *sessions
	verifier      Verifier
	httpClient    *http.Client
	returnOrigins map[string]bool
	secure        bool
}

// newOAuthFlow builds the flow.
//
// secure is derived from PublicURL's scheme rather than configured
// separately: the cookies below are only marked Secure when this app is
// actually reachable over https, because a Secure cookie is never sent back
// over a plain-http deployment and the flow would silently never complete.
func newOAuthFlow(opts OAuthOptions, sessionStore *sessions, verifier Verifier) *oauthFlow {
	base := strings.TrimRight(opts.GitLabURL, "/")

	origins := map[string]bool{}

	for _, origin := range append([]string{opts.PublicURL}, opts.ReturnOrigins...) {
		if normalized, ok := originOf(origin); ok {
			origins[normalized] = true
		}
	}

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
		httpClient: &http.Client{
			Timeout:   tokenEndpointTimeout,
			Transport: metrics.Transport("oauth", http.DefaultTransport),
		},
		returnOrigins: origins,
		secure:        strings.HasPrefix(strings.ToLower(opts.PublicURL), "https://"),
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
// finish the exchange when GitLab sends it back, and where it asked to land
// afterwards.
//
// A return_to that is not allowed is refused here, before the browser goes
// anywhere, rather than ignored: a frontend passing a wrong URL should find
// out when it is developed, not by its users landing somewhere unexpected.
func (f *oauthFlow) handleLogin(resp http.ResponseWriter, req *http.Request) {
	returnTo, err := f.returnTo(req.URL.Query().Get(returnToParam))
	if err != nil {
		writeError(resp, http.StatusBadRequest, err.Error())

		return
	}

	state, err := randomToken()
	if err != nil {
		zap.L().Error("failed to start an oauth login", zap.Error(err))
		writeError(resp, http.StatusInternalServerError, "internal error")

		return
	}

	verifier := oauth2.GenerateVerifier()

	f.setFlowCookie(resp, stateCookie, state)
	f.setFlowCookie(resp, verifierCookie, verifier)

	if returnTo != "" {
		// Encoded so that whatever characters the URL holds survive being a
		// cookie value.
		f.setFlowCookie(resp, returnCookie, base64.RawURLEncoding.EncodeToString([]byte(returnTo)))
	}

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
		zap.L().Info("gitlab refused an oauth login", zap.String("error", gitlabErr))
		writeError(resp, http.StatusUnauthorized, "gitlab refused the login")

		return
	}

	code, err := f.validateCallback(req)
	if err != nil {
		f.clearFlowCookies(resp)
		zap.L().Warn("rejected an oauth callback", zap.Error(err))
		writeError(resp, http.StatusBadRequest, "invalid login callback")

		return
	}

	verifier, err := req.Cookie(verifierCookie)
	if err != nil {
		f.clearFlowCookies(resp)
		writeError(resp, http.StatusBadRequest, "login expired, start again")

		return
	}

	token, err := f.config.Exchange(f.tokenContext(req.Context()), code, oauth2.VerifierOption(verifier.Value))
	if err != nil {
		f.clearFlowCookies(resp)
		zap.L().Warn("failed to exchange an oauth authorization code", zap.Error(err))
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
// that produced a credential GitLab will not honor, or one for an account
// outside the allowed group, fails at the login rather than on the next
// request. It also yields the username, for the default landing page: with
// no return_to, the most useful place to land somebody who just logged in
// is their own record.
func (f *oauthFlow) establish(resp http.ResponseWriter, req *http.Request, token *oauth2.Token) {
	identity, err := f.verifier.Verify(req.Context(), token.AccessToken)
	if errors.Is(err, ErrForbidden) {
		f.clearFlowCookies(resp)
		zap.L().Info("refused a login from outside the allowed group", zap.Error(err))
		writeError(resp, http.StatusForbidden, "this GitLab account is not allowed to use this app")

		return
	}

	if err != nil {
		f.clearFlowCookies(resp)
		zap.L().Warn("gitlab issued a token it would not then honor", zap.Error(err))
		writeError(resp, http.StatusBadGateway, "could not complete the login with gitlab")

		return
	}

	sessionID, expiresAt, err := f.sessions.create(req.Context(), tokensOf(token))
	if err != nil {
		f.clearFlowCookies(resp)
		zap.L().Error("failed to open a session", zap.Error(err))
		writeError(resp, http.StatusInternalServerError, "internal error")

		return
	}

	landing := PathPrefix + "users/" + url.PathEscape(identity.Username)

	if returnTo := f.stashedReturnTo(req); returnTo != "" {
		landing = returnTo
	}

	f.clearFlowCookies(resp)
	http.SetCookie(resp, f.cookie(SessionCookie, sessionID, int(time.Until(expiresAt).Seconds())))

	// landing is either this app's own path or a URL returnTo checked
	// against the allowed origins, so it cannot redirect anywhere else.
	http.Redirect(resp, req, landing, http.StatusFound) //nolint:gosec // see above
}

// stashedReturnTo reads back the destination handleLogin stashed.
//
// It is checked again rather than trusted: the cookie came back from the
// browser, and only what handleLogin would have accepted is used.
func (f *oauthFlow) stashedReturnTo(req *http.Request) string {
	cookie, err := req.Cookie(returnCookie)
	if err != nil {
		return ""
	}

	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ""
	}

	returnTo, err := f.returnTo(string(raw))
	if err != nil {
		return ""
	}

	return returnTo
}

// refresh renews a session's tokens with GitLab, for the authenticator.
func (f *oauthFlow) refresh(ctx context.Context, refreshToken string) (sessionTokens, error) {
	// An expired token holding only the refresh token is what makes the
	// token source go to GitLab rather than hand the token straight back.
	stale := &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Unix(0, 0)}

	token, err := f.config.TokenSource(f.tokenContext(ctx), stale).Token()
	if err != nil {
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) && refreshRefused(retrieve) {
			return sessionTokens{}, fmt.Errorf("%w: %s", errRefreshRejected, retrieve.ErrorCode)
		}

		return sessionTokens{}, fmt.Errorf("failed to refresh the session token: %w", err)
	}

	return tokensOf(token), nil
}

// refreshRefused reports whether GitLab's answer to a refresh means the
// refresh token is dead, as opposed to GitLab failing to answer properly.
// OAuth says invalid_grant; a 400 or 401 without an error code is treated
// the same, since retrying it would only be refused again.
func refreshRefused(retrieve *oauth2.RetrieveError) bool {
	if retrieve.ErrorCode == "invalid_grant" {
		return true
	}

	if retrieve.Response == nil {
		return false
	}

	return retrieve.Response.StatusCode == http.StatusBadRequest || retrieve.Response.StatusCode == http.StatusUnauthorized
}

// tokenContext hands the oauth2 library the client it should call GitLab's
// token endpoint with. Left to itself it would use http.DefaultClient, which
// has no timeout at all.
func (f *oauthFlow) tokenContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, f.httpClient)
}

// tokensOf is what a session keeps of an OAuth token.
func tokensOf(token *oauth2.Token) sessionTokens {
	return sessionTokens{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		Expiry:       token.Expiry,
	}
}

// returnTo resolves where a browser may be sent after logging in, or the
// empty string for the default.
//
// A path on this app is always allowed. An absolute URL is allowed only on
// an origin the operator listed (or PublicURL's own), which is what keeps
// the login from becoming an open redirect that hands a phishing page a
// link starting with this app's trusted address. Protocol-relative and
// backslash forms are refused because browsers resolve "//evil.example" and
// "/\evil.example" to another host while they look like paths.
func (f *oauthFlow) returnTo(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	if strings.ContainsAny(raw, "\\\r\n\t") || strings.HasPrefix(raw, "//") {
		return "", errReturnTo
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errReturnTo
	}

	if parsed.Scheme == "" && parsed.Host == "" && strings.HasPrefix(raw, "/") {
		return parsed.String(), nil
	}

	origin, ok := originOf(raw)
	if !ok || !f.returnOrigins[origin] {
		return "", errReturnTo
	}

	return parsed.String(), nil
}

// originOf returns the scheme://host of an absolute http(s) URL.
func originOf(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", false
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}

	return scheme + "://" + strings.ToLower(parsed.Host), true
}

// handleLogout ends the session the cookie names and clears it.
//
// It is a POST because it changes state, which keeps a link or a prefetch
// from logging somebody out.
func (f *oauthFlow) handleLogout(resp http.ResponseWriter, req *http.Request) {
	cookie, err := req.Cookie(SessionCookie)
	if err == nil {
		f.forgetIdentity(req.Context(), cookie.Value)

		destroyErr := f.sessions.destroy(req.Context(), cookie.Value)
		if destroyErr != nil {
			zap.L().Warn("failed to delete a session on logout", zap.Error(destroyErr))
		}
	}

	// Cleared regardless: a session row that could not be deleted is worse
	// left paired with a cookie that still points at it.
	http.SetCookie(resp, f.cookie(SessionCookie, "", -1))
	resp.WriteHeader(http.StatusNoContent)
}

// forgetIdentity drops a session's token from the identity cache, if there
// is one, so the identity it vouched for is not still honored for the rest
// of the cache window.
func (f *oauthFlow) forgetIdentity(ctx context.Context, sessionID string) {
	cache, ok := f.verifier.(*cachingVerifier)
	if !ok {
		return
	}

	tokens, err := f.sessions.load(ctx, sessionID)
	if err == nil {
		cache.forget(tokens.AccessToken)
	}
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
	for _, name := range []string{stateCookie, verifierCookie, returnCookie} {
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

// randomToken returns a URL-safe random value for use as a CSRF nonce.
func randomToken() (string, error) {
	raw := make([]byte, stateBytes)

	_, err := rand.Read(raw)
	if err != nil {
		return "", err //nolint:wrapcheck // wrapped by the caller with the operation that needed it
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}
