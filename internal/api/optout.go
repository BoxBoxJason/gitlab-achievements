package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// OptOutPath is where a user reads and changes their own opt-out.
const OptOutPath = PathPrefix + "me/opt-out"

// Exclusions is what the opt-out endpoints act through, satisfied by
// *exclusion.Filter.
type Exclusions interface {
	AllowsOptOut() bool
	OptOut(ctx context.Context, gitlabUserID int64, username string) error
	OptIn(ctx context.Context, gitlabUserID int64, username string) error
	Status(ctx context.Context, gitlabUserID int64, username string) (exclusion.Reason, *time.Time, error)
}

// OptOutStatus is the caller's own participation, as the opt-out endpoints
// report it.
//
// Excluded and Reason are reported alongside OptedOut because a user can be
// left out for reasons that are not theirs to change (an operator's list, a
// bot account), and somebody wondering why they are not on the leaderboard
// should learn that rather than be told only that they did not opt out.
type OptOutStatus struct {
	OptedOutAt *time.Time `json:"opted_out_at"`
	Reason     string     `json:"reason,omitempty"`
	OptedOut   bool       `json:"opted_out"`
	Excluded   bool       `json:"excluded"`
	Allowed    bool       `json:"opt_out_allowed"`
}

// optOutRoutes registers the opt-out endpoints.
//
// GET reports, POST opts out, DELETE opts back in. Opting back in stays
// available when opting out has been disabled, so somebody who opted out
// before the operator turned it off is not stuck that way.
func (a *API) optOutRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+OptOutPath, a.handleOptOutStatus)
	mux.HandleFunc("POST "+OptOutPath, a.handleOptOut)
	mux.HandleFunc("DELETE "+OptOutPath, a.handleOptIn)
}

func (a *API) handleOptOutStatus(resp http.ResponseWriter, req *http.Request) {
	identity, ok := a.optOutCaller(resp, req)
	if !ok {
		return
	}

	a.writeOptOutStatus(resp, req, identity)
}

func (a *API) handleOptOut(resp http.ResponseWriter, req *http.Request) {
	identity, ok := a.optOutCaller(resp, req)
	if !ok {
		return
	}

	err := a.exclusions.OptOut(req.Context(), identity.ID, identity.Username)
	if errors.Is(err, exclusion.ErrOptOutDisabled) {
		writeError(resp, http.StatusForbidden, err.Error())

		return
	}

	if err != nil {
		a.fail(resp, req, err)

		return
	}

	zap.L().Info("user opted out of achievements", zap.Int64("gitlab_user_id", identity.ID))
	a.writeOptOutStatus(resp, req, identity)
}

func (a *API) handleOptIn(resp http.ResponseWriter, req *http.Request) {
	identity, ok := a.optOutCaller(resp, req)
	if !ok {
		return
	}

	err := a.exclusions.OptIn(req.Context(), identity.ID, identity.Username)
	if err != nil {
		a.fail(resp, req, err)

		return
	}

	zap.L().Info("user opted back in to achievements", zap.Int64("gitlab_user_id", identity.ID))
	a.writeOptOutStatus(resp, req, identity)
}

// optOutCaller resolves who is asking, answering the request itself when
// nobody can be.
//
// Without authentication there is no caller to act for, so the endpoints
// report themselves unavailable rather than pretend: an operator wanting
// opt-out on an unauthenticated deployment lists the user in
// EXCLUDE_USERS instead.
func (a *API) optOutCaller(resp http.ResponseWriter, req *http.Request) (*gitlabclient.Identity, bool) {
	if a.exclusions == nil {
		writeError(resp, http.StatusNotFound, "opting out is not available on this instance")

		return nil, false
	}

	identity, ok := IdentityFrom(req.Context())
	if !ok {
		writeError(resp, http.StatusNotFound, "opting out requires API_AUTH=gitlab, so the API knows who is asking")

		return nil, false
	}

	return identity, true
}

// writeOptOutStatus answers with the caller's current participation.
func (a *API) writeOptOutStatus(resp http.ResponseWriter, req *http.Request, identity *gitlabclient.Identity) {
	reason, optedOutAt, err := a.exclusions.Status(req.Context(), identity.ID, identity.Username)
	if err != nil {
		a.fail(resp, req, err)

		return
	}

	a.writeJSON(resp, OptOutStatus{
		OptedOut:   optedOutAt != nil,
		OptedOutAt: optedOutAt,
		Excluded:   reason != exclusion.Included,
		Reason:     string(reason),
		Allowed:    a.exclusions.AllowsOptOut(),
	})
}
