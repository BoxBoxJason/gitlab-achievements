package gitlabclient

import (
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// GetCommit retrieves a single commit by SHA.
func (c *ReadClient) GetCommit(pid any, sha string, opt *gitlab.GetCommitOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Commit, error) {
	commit, _, err := c.raw.Commits.GetCommit(pid, sha, opt, options...)

	return commit, err
}

// ListCommits iterates every commit matching opt. Set opt.Pagination =
// "keyset" to use keyset pagination for large repositories; the iterator
// follows whichever pagination style the response returns.
func (c *ReadClient) ListCommits(pid any, opt *gitlab.ListCommitsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Commit, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Commit, *gitlab.Response, error) {
		return c.raw.Commits.ListCommits(pid, opt, withExtra(options, reqOpts...)...)
	})
}
