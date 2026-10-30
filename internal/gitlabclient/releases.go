package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
)

// GetRelease retrieves a single release by the tag it was made from.
func (c *ReadClient) GetRelease(pid any, tagName string, options ...gitlab.RequestOptionFunc) (*gitlab.Release, error) {
	release, _, err := c.raw.Releases.GetRelease(pid, tagName, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get release: %w", err)
	}

	return release, nil
}

// ListReleases iterates every release in a project matching opt.
func (c *ReadClient) ListReleases(pid any, opt *gitlab.ListReleasesOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Release, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Release, *gitlab.Response, error) {
		return c.raw.Releases.ListReleases(pid, opt, withExtra(options, reqOpts...)...)
	})
}
