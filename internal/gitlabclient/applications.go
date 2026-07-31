package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// OAuthScopeReadUser is the only scope this app asks for on behalf of a
// visitor: enough to learn who they are, and nothing else. Identity is the
// whole of what the read API needs, since any authenticated user may read
// any of it.
const OAuthScopeReadUser = "read_user"

// OAuthApplication is an OAuth application registered on the instance.
//
// Secret is populated only by CreateApplication, and only on the call that
// created a confidential application: GitLab returns it once, at creation,
// and never again. ListApplications therefore cannot recover it, which is
// why this app registers itself as a public (non-confidential) application
// and relies on PKCE instead of a secret it would have to store.
type OAuthApplication struct {
	ClientID     string
	Secret       string
	Name         string
	CallbackURL  string
	ID           int64
	Confidential bool
}

// ListOAuthApplications iterates every OAuth application registered on the
// instance. It requires an instance-admin token.
func (c *WriteClient) ListOAuthApplications(options ...gitlab.RequestOptionFunc) iter.Seq2[*OAuthApplication, error] {
	pages := iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Application, *gitlab.Response, error) {
		return c.raw.Applications.ListApplications(&gitlab.ListApplicationsOptions{}, withExtra(options, reqOpts...)...)
	})

	return func(yield func(*OAuthApplication, error) bool) {
		for app, err := range pages {
			if err != nil {
				yield(nil, fmt.Errorf("failed to list oauth applications: %w", err))

				return
			}

			if !yield(convertApplication(app), nil) {
				return
			}
		}
	}
}

// CreateOAuthApplication registers a new OAuth application on the instance.
// It requires an instance-admin token.
//
// A non-confidential application has no client secret, so nothing returned
// here needs storing anywhere secret; the authorization code is protected
// by PKCE instead.
func (c *WriteClient) CreateOAuthApplication(name, redirectURI, scopes string, confidential bool, options ...gitlab.RequestOptionFunc) (*OAuthApplication, error) {
	app, _, err := c.raw.Applications.CreateApplication(&gitlab.CreateApplicationOptions{
		Name:         &name,
		RedirectURI:  &redirectURI,
		Scopes:       &scopes,
		Confidential: &confidential,
	}, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to create oauth application %q: %w", name, err)
	}

	return convertApplication(app), nil
}

// convertApplication maps GitLab's representation onto this package's, so
// callers never handle the client library's types directly.
func convertApplication(app *gitlab.Application) *OAuthApplication {
	if app == nil {
		return nil
	}

	return &OAuthApplication{
		ClientID:     app.ApplicationID,
		Secret:       app.Secret,
		Name:         app.ApplicationName,
		CallbackURL:  app.CallbackURL,
		ID:           app.ID,
		Confidential: app.Confidential,
	}
}
