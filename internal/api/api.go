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
// call GitLab, on every request, so with authentication enabled the API's
// availability is coupled to the instance's; see Verifier.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// PruneInterval is how often the serving process should call PruneSessions.
const PruneInterval = pruneInterval

const (
	// PathPrefix is the subtree the read API is served under. Every route
	// this app adds later, including any that writes, belongs under this
	// prefix and behind the same authentication rather than inventing its
	// own.
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
	store    *store
	oauth    *oauthFlow
	auth     *authenticator
	sessions *sessions
	logger   *zap.Logger
	mux      *http.ServeMux
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
	// Logger receives server-side detail that is deliberately kept out of
	// response bodies. It may be nil.
	Logger *zap.Logger
}

// New builds an API reading from conn.
func New(conn *gorm.DB, opts Options) *API {
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	sessionStore := &sessions{conn: conn}

	api := &API{
		store:    &store{conn: conn},
		sessions: sessionStore,
		logger:   logger,
		mux:      http.NewServeMux(),
	}

	if opts.Verifier != nil {
		api.auth = &authenticator{verifier: opts.Verifier, sessions: sessionStore, logger: logger}
	}

	if opts.OAuth != nil && opts.Verifier != nil {
		api.oauth = newOAuthFlow(*opts.OAuth, sessionStore, opts.Verifier, logger)
	}

	api.routes()

	return api
}

// Handler returns the API's routes.
func (a *API) Handler() http.Handler {
	return a.mux
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
func (a *API) routes() {
	read := http.NewServeMux()
	read.HandleFunc("GET "+PathPrefix+"users/{ref}", a.handleUser)
	read.HandleFunc("GET "+PathPrefix+"users/{ref}/exp", a.handleUserEXP)
	read.HandleFunc("GET "+PathPrefix+"leaderboard", a.handleLeaderboard)

	a.mux.Handle(PathPrefix, a.protect(read))

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

	a.writeJSON(resp, http.StatusOK, detail)
}

// handleUserEXP serves a user's EXP total alone, for the callers that only
// ever wanted the number.
func (a *API) handleUserEXP(resp http.ResponseWriter, req *http.Request) {
	summary, err := a.store.Summary(req.Context(), req.PathValue("ref"))
	if err != nil {
		a.fail(resp, req, err)

		return
	}

	a.writeJSON(resp, http.StatusOK, summary)
}

// handleLeaderboard serves the top users by EXP.
func (a *API) handleLeaderboard(resp http.ResponseWriter, req *http.Request) {
	limit, err := leaderboardLimit(req.URL.Query().Get("limit"))
	if err != nil {
		writeError(resp, http.StatusBadRequest, err.Error())

		return
	}

	board, err := a.store.Leaderboard(req.Context(), limit)
	if err != nil {
		a.fail(resp, req, err)

		return
	}

	a.writeJSON(resp, http.StatusOK, board)
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

	a.logger.Error("failed to serve an api request",
		zap.String("path", req.URL.Path),
		zap.Error(err),
	)
	writeError(resp, http.StatusInternalServerError, "internal error")
}

// writeJSON encodes a successful response.
//
// The status is written before encoding, so an encoder failure part way
// through a body cannot also try to change the status; it is logged
// instead, because by then the client already has a 200 and part of a
// document, and there is nothing better to do about it.
func (a *API) writeJSON(resp http.ResponseWriter, status int, payload any) {
	resp.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp.WriteHeader(status)

	err := json.NewEncoder(resp).Encode(payload)
	if err != nil {
		a.logger.Warn("failed to write an api response body", zap.Error(err))
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
