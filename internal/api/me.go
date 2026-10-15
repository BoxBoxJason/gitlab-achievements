package api

import (
	"errors"
	"net/http"
)

// MePath is where a signed-in caller learns who they are.
const MePath = PathPrefix + "me"

// Me is the caller's own identity, and their record when they have one.
//
// Record is null for somebody this app has no activity for yet, or who is
// left out of achievements, which is the same 404 /users/{ref} would
// answer, folded into a 200 here: being signed in is the question this
// endpoint answers, and a frontend asking it should not have to treat
// "signed in, nothing earned yet" as an error.
type Me struct {
	Record        *Detail       `json:"record"`
	Participation *OptOutStatus `json:"participation,omitempty"`
	Username      string        `json:"username"`
	AvatarURL     string        `json:"avatar_url"`
	GitLabUserID  int64         `json:"gitlab_user_id"`
}

// handleMe serves the caller's identity.
//
// Without authentication there is no caller to describe, so it is a 404,
// like the opt-out endpoints. A frontend can call it on load to decide
// whether to show a login button: 401 means not signed in, 200 means
// signed in, 404 means this deployment has no sign-in at all.
func (a *API) handleMe(resp http.ResponseWriter, req *http.Request) {
	identity, ok := IdentityFrom(req.Context())
	if !ok {
		writeError(resp, http.StatusNotFound, "there is no signing in on this instance (API_AUTH=none)")

		return
	}

	caller := Me{Username: identity.Username, AvatarURL: identity.AvatarURL, GitLabUserID: identity.ID}

	detail, err := a.store.DetailByGitLabID(req.Context(), identity.ID)

	switch {
	case err == nil:
		caller.Record = detail
	case !errors.Is(err, ErrUserUnknown):
		a.fail(resp, req, err)

		return
	}

	if a.exclusions != nil {
		reason, optedOutAt, statusErr := a.exclusions.Status(req.Context(), identity.ID, identity.Username)
		if statusErr != nil {
			a.fail(resp, req, statusErr)

			return
		}

		caller.Participation = participation(a.exclusions, reason, optedOutAt)
	}

	a.writeJSON(resp, caller)
}
