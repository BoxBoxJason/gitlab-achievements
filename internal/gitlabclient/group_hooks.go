//nolint:dupl // GitLab's group and project hook APIs are the same four calls over different types; collapsing the two wrappers into one generic would obscure which endpoint each reaches for no gain
package gitlabclient

import (
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ListGroupHooks lists every webhook registered on one group, following
// pagination to the end rather than returning only the first page.
//
// A group is not expected to carry more hooks than a single page holds,
// and GitLab's plan limits keep it that way, but a scan that silently
// stopped at 100 would adopt nothing and register a duplicate hook on
// every sweep. The result is collected rather than streamed because the
// caller scans it whole, and it is bounded by those same limits.
func (c *WriteClient) ListGroupHooks(gid any, opt *gitlab.ListGroupHooksOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.GroupHook, error) {
	var hooks []*gitlab.GroupHook

	for hook, err := range iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.GroupHook, *gitlab.Response, error) {
		return c.raw.Groups.ListGroupHooks(gid, opt, withExtra(options, reqOpts...)...)
	}) {
		if err != nil {
			return nil, fmt.Errorf("failed to list group hooks: %w", err)
		}

		hooks = append(hooks, hook)
	}

	return hooks, nil
}

// AddGroupHook registers a new webhook on a group.
func (c *WriteClient) AddGroupHook(gid any, opt *gitlab.AddGroupHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.GroupHook, error) {
	hook, _, err := c.raw.Groups.AddGroupHook(gid, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to add group hook: %w", err)
	}

	return hook, nil
}

// EditGroupHook updates an existing group webhook.
func (c *WriteClient) EditGroupHook(gid any, hook int64, opt *gitlab.EditGroupHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.GroupHook, error) {
	h, _, err := c.raw.Groups.EditGroupHook(gid, hook, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to edit group hook: %w", err)
	}

	return h, nil
}

// DeleteGroupHook removes a group webhook. The wrapped error keeps
// gitlab.ErrNotFound intact so callers can tell "already gone" apart from a
// failure to remove it.
func (c *WriteClient) DeleteGroupHook(gid any, hook int64, options ...gitlab.RequestOptionFunc) error {
	_, err := c.raw.Groups.DeleteGroupHook(gid, hook, options...)
	if err != nil {
		return fmt.Errorf("failed to delete group hook: %w", err)
	}

	return nil
}
