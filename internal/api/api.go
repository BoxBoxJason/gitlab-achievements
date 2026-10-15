// Package api serves this app's read API: a user's EXP total, the criteria
// counters and achievement tiers behind it, and the instance leaderboard.
//
// GitLab shows which achievements somebody holds but has no notion of EXP,
// so the totals here exist nowhere else. Everything served is read from the
// local database alone, with no GitLab call on the data path, so the API
// keeps answering while the instance it mirrors is down or rate-limiting.
//
// Authentication is optional and off by default (see config.APIAuth). Where
// it is on, GitLab is the identity provider: a caller presents a personal
// access token or an OAuth access token, or arrives with a session cookie
// from the OAuth flow in oauth.go. Note that verifying a credential does
// call GitLab, at most once per identity cache TTL for each credential, so
// with authentication enabled the API's availability is coupled to the
// instance's; see Verifier.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
)

// PruneInterval is how often the serving process should call PruneSessions.
const PruneInterval = pruneInterval

const (
	// PathPrefix is the subtree the read API is served under. Every route
	// this app adds later, including any that writes, belongs under this
	// prefix and behind the same authentication rather than inventing its
	// own. The one exception is the achievement icon; see routes.
	PathPrefix = "/api/v1/"
	// OAuthPathPrefix is the subtree the OAuth login flow is served under.
	// It sits outside PathPrefix because it is how a browser acquires a
	// credential, so it cannot itself require one.
	OAuthPathPrefix = "/oauth/"

	// defaultLeaderboardLimit is how many users the leaderboard returns
	// when the caller doesn't say.
	defaultLeaderboardLimit = 10
	// maxLeaderboardLimit caps what a caller may ask for, so one request
	// cannot ask the database for every user on the instance.
	maxLeaderboardLimit = 100
)

// API serves the read endpoints and the OAuth login flow.
type API struct {
	exclusions Exclusions
	store      *store
	oauth      *oauthFlow
	auth       *authenticator
	sessions   *sessions
	mux        *http.ServeMux
	icons      map[string]icon
	instance   Instance
}

// Options configures an API.
type Options struct {
	// Verifier resolves caller credentials to GitLab identities. Leaving it
	// nil serves the API unauthenticated, which is the default posture.
	Verifier Verifier
	// OAuth configures the login flow. Leaving it nil serves the read
	// endpoints without one, which is what a deployment using personal
	// access tokens (or no authentication at all) wants.
	OAuth *OAuthOptions
	// Exclusions backs the opt-out endpoints. Leaving it nil answers them
	// 404.
	Exclusions Exclusions
	// IsMember restricts the API to members of one GitLab group (and
	// instance administrators). Leaving it nil admits any GitLab user.
	IsMember MembershipChecker
	// SessionSecret keys the encryption of the GitLab tokens sessions hold.
	// Left empty, a random key is used, which is only fit for tests: every
	// session would end with the process.
	SessionSecret string
	// Instance is what /instance reports about the deployment.
	Instance Instance
	// SessionLifetime is how long a browser stays logged in; zero means
	// config.DefaultSessionLifetime.
	SessionLifetime time.Duration
	// IdentityCacheTTL is how long a verified credential is trusted without
	// asking GitLab again; zero or less disables the cache.
	IdentityCacheTTL time.Duration
}

// New builds an API reading from conn.
func New(conn *gorm.DB, opts Options) (*API, error) {
	sessionStore, err := newSessionStore(conn, opts)
	if err != nil {
		return nil, err
	}

	api := &API{
		store:      &store{conn: conn},
		sessions:   sessionStore,
		mux:        http.NewServeMux(),
		exclusions: opts.Exclusions,
		icons:      loadIcons(),
		instance:   opts.Instance,
	}

	if opts.Verifier != nil {
		verifier := opts.Verifier
		if opts.IsMember != nil {
			verifier = &authorizingVerifier{inner: verifier, isMember: opts.IsMember}
		}

		verifier = newCachingVerifier(verifier, opts.IdentityCacheTTL)

		api.auth = &authenticator{verifier: verifier, sessions: sessionStore}

		if opts.OAuth != nil {
			api.oauth = newOAuthFlow(*opts.OAuth, sessionStore, verifier)
			api.auth.refresher = api.oauth
		}
	}

	api.routes()

	return api, nil
}

// newSessionStore builds the session store with its token encryption.
func newSessionStore(conn *gorm.DB, opts Options) (*sessions, error) {
	secret := opts.SessionSecret
	if secret == "" {
		random, err := randomToken()
		if err != nil {
			return nil, fmt.Errorf("failed to generate a session key: %w", err)
		}

		secret = random
	}

	tokenSealer, err := newSealer(secret)
	if err != nil {
		return nil, err
	}

	lifetime := opts.SessionLifetime
	if lifetime <= 0 {
		lifetime = config.DefaultSessionLifetime
	}

	return &sessions{conn: conn, sealer: tokenSealer, lifetime: lifetime}, nil
}

// Handler returns the API's routes.
//
// Every state-changing request is checked for cross-origin forgery
// (http.CrossOriginProtection, which reads Sec-Fetch-Site and Origin). The
// session cookie is SameSite=Lax, which already keeps it off cross-site
// POSTs, but not off ones from a sibling subdomain, which browsers count as
// same-site. Requests carrying neither header, as scripts using a bearer
// token do, are let through: they cannot have been forged by a browser.
func (a *API) Handler() http.Handler {
	return http.NewCrossOriginProtection().Handler(a.mux)
}

// PruneSessions deletes sessions that have expired, returning how many went.
//
// Expired sessions are already refused, so this is housekeeping: without it
// the table grows by one row per login that was never logged out of. The
// serving process runs it on PruneInterval.
func (a *API) PruneSessions(ctx context.Context) (int64, error) {
	return a.sessions.prune(ctx)
}

// routes registers the API's endpoints.
//
// The read endpoints go behind authentication where it is configured; the
// OAuth routes never do, since they are how a browser gets a credential in
// the first place.
//
// Nor does the achievement icon. A page shows it with an <img> tag, which
// cannot send an Authorization header, so behind a bearer token it would
// never load; behind the session cookie, every icon on a page would cost a
// GitLab round trip to verify the cookie. It gives nothing away either:
// the images are compiled into the binary, the same set for every
// instance, and GitLab already shows them on profiles. Being public is
// also what lets its Cache-Control say public. The route is more specific
// than PathPrefix, so ServeMux sends icon requests to it.
func (a *API) routes() {
	read := http.NewServeMux()
	read.HandleFunc("GET "+PathPrefix+"users/{ref}", a.handleUser)
	read.HandleFunc("GET "+PathPrefix+"users/{ref}/exp", a.handleUserEXP)
	read.HandleFunc("GET "+PathPrefix+"leaderboard", a.handleLeaderboard)
	read.HandleFunc("GET "+PathPrefix+"leaderboard/criteria/{criteria}", a.handleCriteriaLeaderboard)
	read.HandleFunc("GET "+PathPrefix+"achievements", a.handleAchievements)
	read.HandleFunc("GET "+MePath, a.handleMe)
	read.HandleFunc("GET "+InstancePath, a.handleInstance)
	a.optOutRoutes(read)

	a.mux.Handle(PathPrefix, a.protect(read))
	a.mux.HandleFunc("GET "+PathPrefix+"achievements/{criteria}/icon", a.handleAchievementIcon)

	if a.oauth != nil {
		a.oauth.routes(a.mux)
	}
}

// protect wraps the read endpoints in authentication, or leaves them open
// when none is configured.
func (a *API) protect(next http.Handler) http.Handler {
	if a.auth == nil {
		return next
	}

	return a.auth.middleware(next)
}

// handleUser serves a user's whole record.
func (a *API) handleUser(resp http.ResponseWriter, req *http.Request) {
	detail, err := a.store.Detail(req.Context(), req.PathValue("ref"))
	if err != nil {
		a.fail(resp, req, err)

		return
	}

	a.writeJSON(resp, detail)
}

// handleUserEXP serves a user's EXP total alone, for the callers that only
// ever wanted the number.
func (a *API) handleUserEXP(resp http.ResponseWriter, req *http.Request) {
	summary, err := a.store.Summary(req.Context(), req.PathValue("ref"))
	if err != nil {
		a.fail(resp, req, err)

		return
	}

	a.writeJSON(resp, summary)
}

// handleLeaderboard serves the top users by EXP.
func (a *API) handleLeaderboard(resp http.ResponseWriter, req *http.Request) {
	slice, err := parsePage(req)
	if err != nil {
		writeError(resp, http.StatusBadRequest, err.Error())

		return
	}

	span, err := parseWindow(req, time.Now())
	if err != nil {
		writeError(resp, http.StatusBadRequest, err.Error())

		return
	}

	board, err := a.store.Leaderboard(req.Context(), slice, span)
	if err != nil {
		a.fail(resp, req, err)

		return
	}

	a.writeJSON(resp, board)
}

// handleCriteriaLeaderboard serves the ranking for one criteria's counter.
//
// It has no period: counters hold running totals, not a history, so there
// is nothing to tell how much of a count was earned inside a window.
func (a *API) handleCriteriaLeaderboard(resp http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()
	if query.Has("since") || (query.Has("period") && query.Get("period") != PeriodAll) {
		writeError(resp, http.StatusBadRequest, "per-criteria rankings cover all time only: counters keep totals, not history")

		return
	}

	slice, err := parsePage(req)
	if err != nil {
		writeError(resp, http.StatusBadRequest, err.Error())

		return
	}

	board, err := a.store.criteriaLeaderboard(req.Context(), req.PathValue("criteria"), slice)
	if err != nil {
		a.fail(resp, req, err)

		return
	}

	a.writeJSON(resp, board)
}

// leaderboardLimit resolves the limit query parameter.
//
// An out-of-range or unparseable value is rejected rather than clamped
// silently: a caller who asked for 5000 and received 100 without being told
// would reasonably conclude the instance has 100 users.
func leaderboardLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLeaderboardLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("limit must be a number")
	}

	if limit < 1 || limit > maxLeaderboardLimit {
		return 0, errors.New("limit must be between 1 and " + strconv.Itoa(maxLeaderboardLimit))
	}

	return limit, nil
}

// fail answers a store error.
//
// An unknown user is the one case a caller can act on, and is 404: it means
// this app has recorded no activity for them at all, which is a different
// answer from a user it knows who has earned nothing. Anything else is a
// database failure, logged in full and reported as a bare 500, since its
// detail would describe this app's internals rather than anything the
// caller did.
func (a *API) fail(resp http.ResponseWriter, req *http.Request, err error) {
	if errors.Is(err, ErrUserUnknown) {
		writeError(resp, http.StatusNotFound, "no activity recorded for this user")

		return
	}

	if errors.Is(err, ErrCriteriaUnknown) {
		writeError(resp, http.StatusNotFound, "no such criteria")

		return
	}

	zap.L().Error("failed to serve an api request",
		zap.String("path", req.URL.Path),
		zap.Error(err),
	)
	writeError(resp, http.StatusInternalServerError, "internal error")
}

// writeJSON encodes a successful response, always a 200: every endpoint
// that succeeds answers with the document it was asked for, and failures
// go through writeError.
//
// The status is written before encoding, so an encoder failure part way
// through a body cannot also try to change the status; it is logged
// instead, because by then the client already has a 200 and part of a
// document, and there is nothing better to do about it.
func (a *API) writeJSON(resp http.ResponseWriter, payload any) {
	resp.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp.WriteHeader(http.StatusOK)

	err := json.NewEncoder(resp).Encode(payload)
	if err != nil {
		zap.L().Warn("failed to write an api response body", zap.Error(err))
	}
}

// errorBody is the shape every error response takes, so a caller can parse
// failures the same way it parses successes.
type errorBody struct {
	Error string `json:"error"`
}

// writeError answers with a JSON error document.
func writeError(resp http.ResponseWriter, status int, message string) {
	resp.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp.WriteHeader(status)

	json.NewEncoder(resp).Encode(errorBody{Error: message}) //nolint:errcheck,errchkjson,gosec // best-effort write to a client that may have disconnected
}
