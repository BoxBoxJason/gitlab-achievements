package api

import "net/http"

// InstancePath is where a frontend learns what this deployment is attached
// to.
const InstancePath = PathPrefix + "instance"

// Instance describes the deployment rather than anybody's record: the GitLab
// instance it mirrors, so a page can link names to profiles there, and the
// version serving it.
//
// It sits behind the same authentication as every other read route. A page
// asks for it once it knows it may read at all, which /me has told it.
type Instance struct {
	GitLabURL string `json:"gitlab_url"`
	Version   string `json:"version"`
}

// handleInstance serves the deployment's description.
func (a *API) handleInstance(resp http.ResponseWriter, _ *http.Request) {
	a.writeJSON(resp, a.instance)
}
