//nolint:dupl // see the note in group_hooks.go: the two hook APIs are deliberately kept as separate wrappers
package gitlabclient

import (
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ListProjectHooks lists every webhook registered on one project,
// following pagination to the end; see ListGroupHooks for why that matters
// on a listing this small.
func (c *WriteClient) ListProjectHooks(pid any, opt *gitlab.ListProjectHooksOptions, options ...gitlab.RequestOptionFunc) ([]*gitlab.ProjectHook, error) {
	var hooks []*gitlab.ProjectHook

	for hook, err := range iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.ProjectHook, *gitlab.Response, error) {
		return c.raw.Projects.ListProjectHooks(pid, opt, withExtra(options, reqOpts...)...)
	}) {
		if err != nil {
			return nil, fmt.Errorf("failed to list project hooks: %w", err)
		}

		hooks = append(hooks, hook)
	}

	return hooks, nil
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

// DeleteProjectHook removes a project webhook; see DeleteGroupHook on why
// the error is wrapped rather than replaced.
func (c *WriteClient) DeleteProjectHook(pid any, hook int64, options ...gitlab.RequestOptionFunc) error {
	_, err := c.raw.Projects.DeleteProjectHook(pid, hook, options...)
	if err != nil {
		return fmt.Errorf("failed to delete project hook: %w", err)
	}

	return nil
}
