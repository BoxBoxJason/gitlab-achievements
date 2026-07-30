package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// GetGroup retrieves a single group by ID or full path.
func (c *ReadClient) GetGroup(gid any, opt *gitlab.GetGroupOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Group, error) {
	g, _, err := c.raw.Groups.GetGroup(gid, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get group: %w", err)
	}

	return g, nil
}

// ListGroups iterates every group matching opt. Set opt.Pagination =
// "keyset" to use keyset pagination for large instances; the iterator
// follows whichever pagination style the response returns.
func (c *ReadClient) ListGroups(opt *gitlab.ListGroupsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Group, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Group, *gitlab.Response, error) {
		return c.raw.Groups.ListGroups(opt, withExtra(options, reqOpts...)...)
	})
}

// ListGroupProjects iterates the projects belonging to one group. Set
// opt.IncludeSubGroups to descend into the group's whole subtree, and
// opt.WithShared to false to exclude projects merely shared into the group,
// which are owned (and enumerated) elsewhere.
func (c *ReadClient) ListGroupProjects(gid any, opt *gitlab.ListGroupProjectsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Project, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Project, *gitlab.Response, error) {
		return c.raw.Groups.ListGroupProjects(gid, opt, withExtra(options, reqOpts...)...)
	})
}
