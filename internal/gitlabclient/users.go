package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// GetUser retrieves a single user by ID.
func (c *ReadClient) GetUser(user int64, opt *gitlab.GetUserOptions, options ...gitlab.RequestOptionFunc) (*gitlab.User, error) {
	u, _, err := c.raw.Users.GetUser(user, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	return u, nil
}

// CurrentUser retrieves the user identity the read token authenticates as.
func (c *ReadClient) CurrentUser(options ...gitlab.RequestOptionFunc) (*gitlab.User, error) {
	u, _, err := c.raw.Users.CurrentUser(options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get current user: %w", err)
	}

	return u, nil
}

// CurrentUser retrieves the user identity the write token authenticates as.
func (c *WriteClient) CurrentUser(options ...gitlab.RequestOptionFunc) (*gitlab.User, error) {
	u, _, err := c.raw.Users.CurrentUser(options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get current user: %w", err)
	}

	return u, nil
}

// ListUsers iterates every user matching opt. Set opt.Pagination = "keyset"
// to use keyset pagination for large instances; the iterator follows
// whichever pagination style the response returns.
func (c *ReadClient) ListUsers(opt *gitlab.ListUsersOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.User, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.User, *gitlab.Response, error) {
		return c.raw.Users.ListUsers(opt, withExtra(options, reqOpts...)...)
	})
}
