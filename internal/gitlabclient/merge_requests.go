package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
)

// GetMergeRequest retrieves a single merge request by its project-scoped
// IID.
func (c *ReadClient) GetMergeRequest(pid any, mergeRequest int64, opt *gitlab.GetMergeRequestsOptions, options ...gitlab.RequestOptionFunc) (*gitlab.MergeRequest, error) {
	mr, _, err := c.raw.MergeRequests.GetMergeRequest(pid, mergeRequest, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get merge request: %w", err)
	}

	return mr, nil
}

// ListProjectMergeRequests iterates every merge request in a project
// matching opt. Set opt.Pagination = "keyset" to use keyset pagination for
// large projects; the iterator follows whichever pagination style the
// response returns.
func (c *ReadClient) ListProjectMergeRequests(pid any, opt *gitlab.ListProjectMergeRequestsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.BasicMergeRequest, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.BasicMergeRequest, *gitlab.Response, error) {
		return c.raw.MergeRequests.ListProjectMergeRequests(pid, opt, withExtra(options, reqOpts...)...)
	})
}

// ListMergeRequestDiscussions iterates every discussion on a merge request,
// resolvable review threads and individual comments alike.
func (c *ReadClient) ListMergeRequestDiscussions(pid any, mergeRequest int64, opt *gitlab.ListMergeRequestDiscussionsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Discussion, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Discussion, *gitlab.Response, error) {
		return c.raw.Discussions.ListMergeRequestDiscussions(pid, mergeRequest, opt, withExtra(options, reqOpts...)...)
	})
}
