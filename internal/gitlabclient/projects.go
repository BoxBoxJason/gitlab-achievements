package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

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
