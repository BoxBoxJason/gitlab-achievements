package gitlabclient

import (
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// GetMergeRequest retrieves a single merge request by its project-scoped
// IID.
func (c *ReadClient) GetMergeRequest(pid any, mergeRequest int64, opt *gitlab.GetMergeRequestsOptions, options ...gitlab.RequestOptionFunc) (*gitlab.MergeRequest, error) {
	mr, _, err := c.raw.MergeRequests.GetMergeRequest(pid, mergeRequest, opt, options...)

	return mr, err
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
