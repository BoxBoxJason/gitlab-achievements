package gitlabclient

import (
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ListSystemHooks lists every system hook registered on the instance.
func (c *WriteClient) ListSystemHooks(options ...gitlab.RequestOptionFunc) ([]*gitlab.Hook, error) {
	hooks, _, err := c.raw.SystemHooks.ListHooks(options...)
	if err != nil {
		return nil, fmt.Errorf("failed to list system hooks: %w", err)
	}

	return hooks, nil
}

// GetSystemHook retrieves a single system hook by ID.
func (c *WriteClient) GetSystemHook(hook int64, options ...gitlab.RequestOptionFunc) (*gitlab.Hook, error) {
	h, _, err := c.raw.SystemHooks.GetHook(hook, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get system hook: %w", err)
	}

	return h, nil
}

// AddSystemHook registers a new system hook.
func (c *WriteClient) AddSystemHook(opt *gitlab.AddHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Hook, error) {
	hook, _, err := c.raw.SystemHooks.AddHook(opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to add system hook: %w", err)
	}

	return hook, nil
}

// EditSystemHook updates an existing system hook.
func (c *WriteClient) EditSystemHook(hook int64, opt *gitlab.EditHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Hook, error) {
	h, _, err := c.raw.SystemHooks.EditHook(hook, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to edit system hook: %w", err)
	}

	return h, nil
}
