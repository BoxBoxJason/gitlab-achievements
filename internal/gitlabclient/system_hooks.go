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

// AddSystemHook registers a new system hook.
func (c *WriteClient) AddSystemHook(opt *gitlab.AddHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Hook, error) {
	hook, _, err := c.raw.SystemHooks.AddHook(opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to add system hook: %w", err)
	}

	return hook, nil
}
