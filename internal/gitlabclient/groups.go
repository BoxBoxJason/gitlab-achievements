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
