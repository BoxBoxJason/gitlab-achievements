package gitlabclient

import (
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ListSystemHooks lists every system hook registered on the instance.
func (c *WriteClient) ListSystemHooks(options ...gitlab.RequestOptionFunc) ([]*gitlab.Hook, error) {
	hooks, _, err := c.raw.SystemHooks.ListHooks(options...)

	return hooks, err
}

// AddSystemHook registers a new system hook.
func (c *WriteClient) AddSystemHook(opt *gitlab.AddHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Hook, error) {
	hook, _, err := c.raw.SystemHooks.AddHook(opt, options...)

	return hook, err
}
