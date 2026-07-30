package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// namespaceKindGroup is what GitLab reports as a project namespace's kind
// when the project belongs to a group rather than to a user.
const namespaceKindGroup = "group"

// ProjectOwnedByGroup reports whether a project lives in a group rather
// than in a user's personal namespace.
//
// This decides which projects the app covers at all, and it lives here
// because two callers have to agree on it exactly: the webhook sweep,
// which registers the hooks live activity arrives through, and the
// historical backfill, which walks the same projects' history. A project
// counted by one and not the other would either earn achievements that
// stop advancing the moment the backfill finishes, or never earn them at
// all.
//
// Personal-namespace projects are the exclusion. A group hook cannot reach
// them, since a personal namespace is not a group, and covering them only
// on instances where project hooks happen to be in use would make a user's
// progress depend on their instance's license.
//
// A project whose namespace GitLab didn't report is excluded rather than
// assumed group-owned: for the sweep, registering a hook is a write, and
// guessing wrong puts one somewhere it doesn't belong.
func ProjectOwnedByGroup(project *gitlab.Project) bool {
	return project != nil && project.Namespace != nil && project.Namespace.Kind == namespaceKindGroup
}

// GetProject retrieves a single project by ID or path.
func (c *ReadClient) GetProject(pid any, opt *gitlab.GetProjectOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Project, error) {
	p, _, err := c.raw.Projects.GetProject(pid, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	return p, nil
}

// ListProjects iterates every project matching opt. Set opt.Pagination =
// "keyset" to use keyset pagination for large instances; the iterator
// follows whichever pagination style the response returns.
func (c *ReadClient) ListProjects(opt *gitlab.ListProjectsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Project, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Project, *gitlab.Response, error) {
		return c.raw.Projects.ListProjects(opt, withExtra(options, reqOpts...)...)
	})
}
