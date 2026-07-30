//nolint:dupl // see the note in group_hooks.go: the two hook APIs are deliberately kept as separate wrappers
package gitlabclient

import (
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ListProjectHooks lists the webhooks registered on one project.
func (c *WriteClient) ListProjectHooks(pid any, opt *gitlab.ListProjectHooksOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.ProjectHook, error) {
	hooks, _, err := c.raw.Projects.ListProjectHooks(pid, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to list project hooks: %w", err)
	}

	return hooks, nil
}

// GetProjectHook retrieves a single project webhook by ID.
func (c *WriteClient) GetProjectHook(pid any, hook int64, options ...gitlab.RequestOptionFunc) (*gitlab.ProjectHook, error) {
	h, _, err := c.raw.Projects.GetProjectHook(pid, hook, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get project hook: %w", err)
	}

	return h, nil
}

// AddProjectHook registers a new webhook on a project.
func (c *WriteClient) AddProjectHook(pid any, opt *gitlab.AddProjectHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.ProjectHook, error) {
	hook, _, err := c.raw.Projects.AddProjectHook(pid, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to add project hook: %w", err)
	}

	return hook, nil
}

// EditProjectHook updates an existing project webhook.
func (c *WriteClient) EditProjectHook(pid any, hook int64, opt *gitlab.EditProjectHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.ProjectHook, error) {
	h, _, err := c.raw.Projects.EditProjectHook(pid, hook, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to edit project hook: %w", err)
	}

	return h, nil
}
