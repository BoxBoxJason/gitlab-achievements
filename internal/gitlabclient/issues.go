package gitlabclient

import (
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// GetIssue retrieves a single issue by its project-scoped IID.
func (c *ReadClient) GetIssue(pid any, issue int64, options ...gitlab.RequestOptionFunc) (*gitlab.Issue, error) {
	i, _, err := c.raw.Issues.GetIssue(pid, issue, options...)

	return i, err
}

// ListProjectIssues iterates every issue in a project matching opt. Set
// opt.Pagination = "keyset" to use keyset pagination for large projects;
// the iterator follows whichever pagination style the response returns.
func (c *ReadClient) ListProjectIssues(pid any, opt *gitlab.ListProjectIssuesOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Issue, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Issue, *gitlab.Response, error) {
		return c.raw.Issues.ListProjectIssues(pid, opt, withExtra(options, reqOpts...)...)
	})
}
