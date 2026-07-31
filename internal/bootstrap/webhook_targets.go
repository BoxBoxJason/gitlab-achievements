package bootstrap

import (
	"context"
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// hookRef is the little of a GitLab hook the recovery scan needs: enough to
// recognize one this app already owns, and to address it.
type hookRef struct {
	url string
	id  int64
}

// hookTarget abstracts the object a webhook is registered on, so the
// register/adopt/heal logic is written once rather than once per scope.
// GitLab's group and project hook APIs are the same four calls over
// different types, and the difference between them is not interesting to
// the synchronization.
type hookTarget interface {
	// scope names which kind of object this is, for the stored hook ID and
	// for log messages.
	scope() db.HookScope
	// id is the GitLab ID of the group or project.
	id() int64
	// list returns every hook currently registered on the target.
	list(ctx context.Context) ([]hookRef, error)
	// add registers a new hook and returns its ID.
	add(ctx context.Context, webhookURL, secret string) (int64, error)
	// edit re-applies the desired configuration to an existing hook and
	// returns its ID, wrapping gitlab.ErrNotFound when the hook is gone.
	edit(ctx context.Context, hookID int64, webhookURL, secret string) (int64, error)
	// remove deletes a hook, wrapping gitlab.ErrNotFound when it is
	// already gone.
	remove(ctx context.Context, hookID int64) error
}

// hookTargetFor builds the target one recorded hook is attached to. The
// sweep knows which kind it is walking and constructs targets directly;
// cleanup reads the scope back off each stored row instead, so that a
// deployment that switched scopes still removes what the old one left
// behind.
func hookTargetFor(write hookManager, scope db.HookScope, targetID int64) hookTarget {
	if scope == db.HookScopeGroup {
		return &groupHookTarget{write: write, groupID: targetID}
	}

	return &projectHookTarget{write: write, projectID: targetID}
}

// groupHookTarget registers this app's hook on one top-level group, which
// covers every project in that group and its subgroups.
type groupHookTarget struct {
	write   groupHookManager
	groupID int64
}

func (t *groupHookTarget) scope() db.HookScope { return db.HookScopeGroup }

func (t *groupHookTarget) id() int64 { return t.groupID }

func (t *groupHookTarget) list(ctx context.Context) ([]hookRef, error) {
	hooks, err := t.write.ListGroupHooks(t.groupID, &gitlab.ListGroupHooksOptions{
		ListOptions: gitlab.ListOptions{PerPage: hookPageSize},
	}, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("group %d: %w", t.groupID, err)
	}

	refs := make([]hookRef, 0, len(hooks))
	for _, hook := range hooks {
		refs = append(refs, hookRef{id: hook.ID, url: hook.URL})
	}

	return refs, nil
}

func (t *groupHookTarget) add(ctx context.Context, webhookURL, secret string) (int64, error) {
	hook, err := t.write.AddGroupHook(t.groupID, addGroupHookOptions(webhookURL, secret), gitlab.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("group %d: %w", t.groupID, err)
	}

	return hook.ID, nil
}

func (t *groupHookTarget) edit(ctx context.Context, hookID int64, webhookURL, secret string) (int64, error) {
	hook, err := t.write.EditGroupHook(t.groupID, hookID, editGroupHookOptions(webhookURL, secret), gitlab.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("group %d: %w", t.groupID, err)
	}

	return hook.ID, nil
}

func (t *groupHookTarget) remove(ctx context.Context, hookID int64) error {
	err := t.write.DeleteGroupHook(t.groupID, hookID, gitlab.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("group %d: %w", t.groupID, err)
	}

	return nil
}

// projectHookTarget registers this app's hook on a single project, the
// fallback for instances where group webhooks are unavailable.
type projectHookTarget struct {
	write     projectHookManager
	projectID int64
}

func (t *projectHookTarget) scope() db.HookScope { return db.HookScopeProject }

func (t *projectHookTarget) id() int64 { return t.projectID }

func (t *projectHookTarget) list(ctx context.Context) ([]hookRef, error) {
	hooks, err := t.write.ListProjectHooks(t.projectID, &gitlab.ListProjectHooksOptions{
		ListOptions: gitlab.ListOptions{PerPage: hookPageSize},
	}, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("project %d: %w", t.projectID, err)
	}

	refs := make([]hookRef, 0, len(hooks))
	for _, hook := range hooks {
		refs = append(refs, hookRef{id: hook.ID, url: hook.URL})
	}

	return refs, nil
}

func (t *projectHookTarget) add(ctx context.Context, webhookURL, secret string) (int64, error) {
	hook, err := t.write.AddProjectHook(t.projectID, addProjectHookOptions(webhookURL, secret), gitlab.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("project %d: %w", t.projectID, err)
	}

	return hook.ID, nil
}

func (t *projectHookTarget) edit(ctx context.Context, hookID int64, webhookURL, secret string) (int64, error) {
	hook, err := t.write.EditProjectHook(t.projectID, hookID, editProjectHookOptions(webhookURL, secret), gitlab.WithContext(ctx))
	if err != nil {
		return 0, fmt.Errorf("project %d: %w", t.projectID, err)
	}

	return hook.ID, nil
}

func (t *projectHookTarget) remove(ctx context.Context, hookID int64) error {
	err := t.write.DeleteProjectHook(t.projectID, hookID, gitlab.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("project %d: %w", t.projectID, err)
	}

	return nil
}
