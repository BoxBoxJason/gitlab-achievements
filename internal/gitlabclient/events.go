package gitlabclient

import (
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ListUserContributionEvents iterates a user's contribution events (GET
// /users/:id/events) matching opt. Set opt.Pagination = "keyset" to use
// keyset pagination; the iterator follows whichever pagination style the
// response returns.
func (c *ReadClient) ListUserContributionEvents(uid any, opt *gitlab.ListContributionEventsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.ContributionEvent, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.ContributionEvent, *gitlab.Response, error) {
		return c.raw.Users.ListUserContributionEvents(uid, opt, withExtra(options, reqOpts...)...)
	})
}

// ListProjectVisibleEvents iterates a project's visible events (GET
// /projects/:id/events) matching opt. Set opt.Pagination = "keyset" to use
// keyset pagination; the iterator follows whichever pagination style the
// response returns.
func (c *ReadClient) ListProjectVisibleEvents(pid any, opt *gitlab.ListProjectVisibleEventsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.ProjectEvent, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.ProjectEvent, *gitlab.Response, error) {
		return c.raw.Events.ListProjectVisibleEvents(pid, opt, withExtra(options, reqOpts...)...)
	})
}
