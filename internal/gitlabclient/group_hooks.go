//nolint:dupl // GitLab's group and project hook APIs are the same four calls over different types; collapsing the two wrappers into one generic would obscure which endpoint each reaches for no gain
package gitlabclient

import (
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ListGroupHooks lists the webhooks registered on one group.
func (c *WriteClient) ListGroupHooks(gid any, opt *gitlab.ListGroupHooksOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.GroupHook, error) {
	hooks, _, err := c.raw.Groups.ListGroupHooks(gid, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to list group hooks: %w", err)
	}

	return hooks, nil
}

// GetGroupHook retrieves a single group webhook by ID.
func (c *WriteClient) GetGroupHook(gid any, hook int64, options ...gitlab.RequestOptionFunc) (*gitlab.GroupHook, error) {
	h, _, err := c.raw.Groups.GetGroupHook(gid, hook, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get group hook: %w", err)
	}

	return h, nil
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
