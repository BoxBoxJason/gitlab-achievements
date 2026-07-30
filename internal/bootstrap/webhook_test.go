package bootstrap

import (
	"errors"
	"iter"
	"net/http"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

const testWebhookURL = "https://achievements.example.com/webhooks/gitlab"

// fakeTargetLister serves a canned instance layout to the sweep.
type fakeTargetLister struct {
	groups      []*gitlab.Group
	projects    []*gitlab.Project
	groupsErr   error
	projectsErr error
	groupsOpt   gitlab.ListGroupsOptions
	projectsOpt gitlab.ListProjectsOptions
}

func (f *fakeTargetLister) ListGroups(opt *gitlab.ListGroupsOptions, _ ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Group, error] {
	f.groupsOpt = *opt

	return func(yield func(*gitlab.Group, error) bool) {
		if f.groupsErr != nil {
			yield(nil, f.groupsErr)

			return
		}

		for _, group := range f.groups {
			if !yield(group, nil) {
				return
			}
		}
	}
}

func (f *fakeTargetLister) ListProjects(opt *gitlab.ListProjectsOptions, _ ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Project, error] {
	f.projectsOpt = *opt

	return func(yield func(*gitlab.Project, error) bool) {
		if f.projectsErr != nil {
			yield(nil, f.projectsErr)

			return
		}

		for _, project := range f.projects {
			if !yield(project, nil) {
				return
			}
		}
	}
}

// groupProject builds a project the sweep covers: one owned by a group.
// Projects in personal namespaces are out of scope on every tier.
func groupProject(id int64) *gitlab.Project {
	return &gitlab.Project{
		ID:        id,
		Namespace: &gitlab.ProjectNamespace{ID: 900 + id, Kind: "group"},
	}
}

// userProject builds a project in a personal namespace, which the sweep
// skips.
func userProject(id int64) *gitlab.Project {
	return &gitlab.Project{
		ID:        id,
		Namespace: &gitlab.ProjectNamespace{ID: 900 + id, Kind: "user"},
	}
}

// fakeHookManager stands in for the write client across all four hook
// APIs, plus the license lookup that decides which of them is used.
//
// Hooks are keyed by target so the fake can answer a Get the way GitLab
// does: a 404 for any ID it doesn't hold, which is how tests simulate an
// out-of-band deletion.
type fakeHookManager struct {
	license    *gitlab.License
	licenseErr error

	groupHooks   map[int64][]*gitlab.GroupHook
	projectHooks map[int64][]*gitlab.ProjectHook

	addErr       error
	editErr      error
	listErr      error
	targetAddErr map[int64]error

	addCalls     int
	editCalls    int
	getCalls     int
	listCalls    int
	licenseCalls int
	lastToken    string

	nextID int64
}

func (f *fakeHookManager) GetLicense(...gitlab.RequestOptionFunc) (*gitlab.License, error) {
	f.licenseCalls++

	if f.licenseErr != nil {
		return nil, f.licenseErr
	}

	return f.license, nil
}

func (f *fakeHookManager) ListGroupHooks(gid any, _ *gitlab.ListGroupHooksOptions, _ ...gitlab.RequestOptionFunc) ([]*gitlab.GroupHook, error) {
	f.listCalls++

	if f.listErr != nil {
		return nil, f.listErr
	}

	groupID, _ := gid.(int64)

	return f.groupHooks[groupID], nil
}

func (f *fakeHookManager) GetGroupHook(gid any, hook int64, _ ...gitlab.RequestOptionFunc) (*gitlab.GroupHook, error) {
	f.getCalls++

	groupID, _ := gid.(int64)

	for _, existing := range f.groupHooks[groupID] {
		if existing.ID == hook {
			return existing, nil
		}
	}

	return nil, gitlab.ErrNotFound
}

func (f *fakeHookManager) AddGroupHook(gid any, opt *gitlab.AddGroupHookOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.GroupHook, error) {
	f.addCalls++

	groupID, _ := gid.(int64)

	if err := f.addError(groupID); err != nil {
		return nil, err
	}

	f.nextID++
	f.lastToken = *opt.Token

	hook := &gitlab.GroupHook{ID: f.nextID, URL: *opt.URL, GroupID: groupID}

	if f.groupHooks == nil {
		f.groupHooks = map[int64][]*gitlab.GroupHook{}
	}

	f.groupHooks[groupID] = append(f.groupHooks[groupID], hook)

	return hook, nil
}

func (f *fakeHookManager) EditGroupHook(gid any, hook int64, opt *gitlab.EditGroupHookOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.GroupHook, error) {
	f.editCalls++

	if f.editErr != nil {
		return nil, f.editErr
	}

	groupID, _ := gid.(int64)
	f.lastToken = *opt.Token

	return &gitlab.GroupHook{ID: hook, URL: *opt.URL, GroupID: groupID}, nil
}

func (f *fakeHookManager) ListProjectHooks(pid any, _ *gitlab.ListProjectHooksOptions, _ ...gitlab.RequestOptionFunc) ([]*gitlab.ProjectHook, error) {
	f.listCalls++

	if f.listErr != nil {
		return nil, f.listErr
	}

	projectID, _ := pid.(int64)

	return f.projectHooks[projectID], nil
}

func (f *fakeHookManager) GetProjectHook(pid any, hook int64, _ ...gitlab.RequestOptionFunc) (*gitlab.ProjectHook, error) {
	f.getCalls++

	projectID, _ := pid.(int64)

	for _, existing := range f.projectHooks[projectID] {
		if existing.ID == hook {
			return existing, nil
		}
	}

	return nil, gitlab.ErrNotFound
}

func (f *fakeHookManager) AddProjectHook(pid any, opt *gitlab.AddProjectHookOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.ProjectHook, error) {
	f.addCalls++

	projectID, _ := pid.(int64)

	if err := f.addError(projectID); err != nil {
		return nil, err
	}

	f.nextID++
	f.lastToken = *opt.Token

	hook := &gitlab.ProjectHook{ID: f.nextID, URL: *opt.URL, ProjectID: projectID}

	if f.projectHooks == nil {
		f.projectHooks = map[int64][]*gitlab.ProjectHook{}
	}

	f.projectHooks[projectID] = append(f.projectHooks[projectID], hook)

	return hook, nil
}

func (f *fakeHookManager) EditProjectHook(pid any, hook int64, opt *gitlab.EditProjectHookOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.ProjectHook, error) {
	f.editCalls++

	if f.editErr != nil {
		return nil, f.editErr
	}

	projectID, _ := pid.(int64)
	f.lastToken = *opt.Token

	return &gitlab.ProjectHook{ID: hook, URL: *opt.URL, ProjectID: projectID}, nil
}

func (f *fakeHookManager) addError(targetID int64) error {
	if err := f.targetAddErr[targetID]; err != nil {
		return err
	}

	return f.addErr
}

func statusErr(code int) error {
	return &gitlab.ErrorResponse{StatusCode: code, Message: http.StatusText(code)}
}

func hookCfg(scope config.HookScope) *config.Config {
	return &config.Config{HookScope: string(scope), WebhookSecret: "s3cr3t"}
}

// twoGroupInstance is a minimal instance: two top-level groups and three
// group-owned projects spread across them.
func twoGroupInstance() *fakeTargetLister {
	return &fakeTargetLister{
		groups:   []*gitlab.Group{{ID: 1}, {ID: 2}},
		projects: []*gitlab.Project{groupProject(10), groupProject(11), groupProject(20)},
	}
}

func TestSyncHooks_RegistersOneHookPerTopLevelGroupOnPaidTiers(t *testing.T) {
	conn := testConn(t)
	read := twoGroupInstance()
	write := &fakeHookManager{license: &gitlab.License{Plan: "premium"}}

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Scope != appdb.HookScopeGroup {
		t.Errorf("expected a premium instance to use group hooks, got %q", report.Scope)
	}

	if report.Targets != 2 || report.Created != 2 {
		t.Errorf("expected one hook per top-level group, got %+v", report)
	}

	// The projects inside those groups must not also be hooked: the group
	// hook already covers them, and a second hook would double every event.
	if len(write.projectHooks) != 0 {
		t.Errorf("expected no project hooks alongside the group ones, got %d", len(write.projectHooks))
	}

	if !*read.groupsOpt.TopLevelOnly {
		t.Error("expected only top-level groups to be listed, since a group hook covers the whole subtree")
	}
}

func TestSyncHooks_RegistersOneHookPerProjectOnFreeTiers(t *testing.T) {
	conn := testConn(t)
	read := twoGroupInstance()
	write := &fakeHookManager{licenseErr: gitlab.ErrNotFound}

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Scope != appdb.HookScopeProject {
		t.Errorf("expected an instance with no license to fall back to project hooks, got %q", report.Scope)
	}

	if report.Targets != 3 || report.Created != 3 {
		t.Errorf("expected one hook per project on the instance, got %+v", report)
	}

	if len(write.groupHooks) != 0 {
		t.Errorf("expected no group hooks on an instance that can't have them, got %d", len(write.groupHooks))
	}

	// Hooks follow activity, which happens instance-wide, so the sweep walks
	// projects directly rather than descending from any particular group.
	if read.groupsOpt.TopLevelOnly != nil {
		t.Error("expected the project sweep not to enumerate groups at all")
	}
}

func TestSyncHooks_FreeTierIsWhatAnInaccessibleLicenseFallsBackTo(t *testing.T) {
	conn := testConn(t)
	write := &fakeHookManager{licenseErr: statusErr(http.StatusForbidden)}

	report, err := syncHooks(t.Context(), &fakeTargetLister{}, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected a token that can't read the license to fall back rather than fail, got: %v", err)
	}

	if report.Scope != appdb.HookScopeProject {
		t.Errorf("expected project hooks when the tier can't be established, got %q", report.Scope)
	}
}

func TestSyncHooks_TransientLicenseFailureStopsTheSweep(t *testing.T) {
	conn := testConn(t)
	write := &fakeHookManager{licenseErr: statusErr(http.StatusInternalServerError)}

	_, err := syncHooks(t.Context(), twoGroupInstance(), write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err == nil {
		t.Fatal("expected a transient license failure to fail rather than silently pick a strategy")
	}
}

func TestSyncHooks_ExplicitScopeSkipsTheLicenseLookup(t *testing.T) {
	conn := testConn(t)
	write := &fakeHookManager{license: &gitlab.License{Plan: "ultimate"}}

	report, err := syncHooks(t.Context(), twoGroupInstance(), write, conn, hookCfg(config.HookScopeProject), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Scope != appdb.HookScopeProject {
		t.Errorf("expected the configured scope to win over the license, got %q", report.Scope)
	}

	if write.licenseCalls != 0 {
		t.Errorf("expected no license lookup when the scope is configured, got %d", write.licenseCalls)
	}
}

func TestSyncHooks_IsIdempotentAcrossSweeps(t *testing.T) {
	conn := testConn(t)
	read := twoGroupInstance()
	write := &fakeHookManager{license: &gitlab.License{Plan: "premium"}}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	firstAdds := write.addCalls

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if write.addCalls != firstAdds {
		t.Errorf("expected the second sweep to register nothing new, got %d adds", write.addCalls-firstAdds)
	}

	if report.Created != 0 || report.Updated != 2 {
		t.Errorf("expected the second sweep to only re-apply configuration, got %+v", report)
	}

	// The stored ID is what keeps a sweep cheap: a direct lookup per target
	// rather than a scan of every hook on the group.
	if write.listCalls != 2 {
		t.Errorf("expected the second sweep to look hooks up by stored id, got %d list calls", write.listCalls)
	}
}

func TestSyncHooks_RecreatesAHookDeletedOutOfBand(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{groups: []*gitlab.Group{{ID: 1}}}
	write := &fakeHookManager{license: &gitlab.License{Plan: "premium"}}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Someone deletes the hook in the GitLab UI.
	write.groupHooks[1] = nil

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created != 1 {
		t.Errorf("expected the deleted hook to be re-registered, got %+v", report)
	}
}

func TestSyncHooks_AdoptsAnExistingHookPointingAtThisApp(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{groups: []*gitlab.Group{{ID: 1}}}
	write := &fakeHookManager{
		license:    &gitlab.License{Plan: "premium"},
		groupHooks: map[int64][]*gitlab.GroupHook{1: {{ID: 77, URL: testWebhookURL}}},
	}

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created != 0 || report.Updated != 1 {
		t.Errorf("expected a hand-configured hook to be adopted rather than duplicated, got %+v", report)
	}

	storedID, found, err := loadHookID(conn, appdb.HookScopeGroup, 1)
	if err != nil || !found || storedID != 77 {
		t.Errorf("expected the adopted hook id to be persisted, got id=%d found=%v err=%v", storedID, found, err)
	}
}

func TestSyncHooks_LeavesUnrelatedHooksAlone(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{groups: []*gitlab.Group{{ID: 1}}}
	write := &fakeHookManager{
		license:    &gitlab.License{Plan: "premium"},
		groupHooks: map[int64][]*gitlab.GroupHook{1: {{ID: 9, URL: "https://someone-elses-tool.example.com/hook"}}},
	}

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created != 1 {
		t.Errorf("expected a hook belonging to another tool to be left alone and a new one added, got %+v", report)
	}

	if write.editCalls != 0 {
		t.Errorf("expected no edits to an unrelated hook, got %d", write.editCalls)
	}
}

func TestSyncHooks_AppliesTheConfiguredSecret(t *testing.T) {
	conn := testConn(t)
	write := &fakeHookManager{license: &gitlab.License{Plan: "premium"}}

	_, err := syncHooks(t.Context(), &fakeTargetLister{groups: []*gitlab.Group{{ID: 1}}}, write, conn,
		hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if write.lastToken != "s3cr3t" {
		t.Errorf("expected the hook to carry the configured secret, got %q", write.lastToken)
	}
}

func TestSyncHooks_SkipsTargetsItCannotManage(t *testing.T) {
	conn := testConn(t)
	read := twoGroupInstance()
	write := &fakeHookManager{
		licenseErr:   gitlab.ErrNotFound,
		targetAddErr: map[int64]error{11: statusErr(http.StatusForbidden)},
	}

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected one unmanageable project not to fail the sweep, got: %v", err)
	}

	if report.Targets != 2 || report.Skipped != 1 {
		t.Errorf("expected 2 hooked projects and 1 skipped, got %+v", report)
	}
}

func TestSyncHooks_StopsOnAnInstanceWideFailure(t *testing.T) {
	conn := testConn(t)
	read := twoGroupInstance()
	write := &fakeHookManager{
		licenseErr: gitlab.ErrNotFound,
		addErr:     statusErr(http.StatusInternalServerError),
	}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err == nil {
		t.Fatal("expected a GitLab-wide failure to stop the sweep rather than skip every target")
	}
}

func TestSyncHooks_StopsWhenGroupsCannotBeListed(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{groupsErr: errors.New("gitlab unreachable")}
	write := &fakeHookManager{license: &gitlab.License{Plan: "premium"}}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err == nil {
		t.Fatal("expected an unlistable instance to fail rather than report an empty sweep as success")
	}
}

func TestSyncHooks_RegistersHooksOnTargetsCreatedSinceTheLastSweep(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{groups: []*gitlab.Group{{ID: 1}}}
	write := &fakeHookManager{license: &gitlab.License{Plan: "premium"}}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Nothing tells this app a group was created, so one that appears later
	// is only picked up by the next sweep.
	read.groups = append(read.groups, &gitlab.Group{ID: 2})

	report, err := ReconcileWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created != 1 || report.Targets != 2 {
		t.Errorf("expected the new group to be hooked by the next sweep, got %+v", report)
	}
}

func TestSyncHooks_CoversProjectsOutsideTheAchievementsNamespace(t *testing.T) {
	conn := testConn(t)

	// The achievements namespace is only where the definitions live and are
	// awarded from. Activity happens everywhere, so hooks have to go
	// everywhere: a project unrelated to that namespace, and one in no group
	// the sweep ever lists, must still be hooked.
	read := &fakeTargetLister{
		groups:   []*gitlab.Group{{ID: 1}},
		projects: []*gitlab.Project{groupProject(10), groupProject(4242)},
	}
	write := &fakeHookManager{licenseErr: gitlab.ErrNotFound}

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Targets != 2 {
		t.Errorf("expected every project on the instance to be hooked, got %+v", report)
	}

	for _, projectID := range []int64{10, 4242} {
		if len(write.projectHooks[projectID]) == 0 {
			t.Errorf("expected project %d to be hooked", projectID)
		}
	}
}

func TestSyncHooks_SkipsProjectsInPersonalNamespaces(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{
		projects: []*gitlab.Project{groupProject(10), userProject(11)},
	}
	write := &fakeHookManager{licenseErr: gitlab.ErrNotFound}

	report, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Targets != 1 {
		t.Errorf("expected the personal-namespace project to be passed over, got %+v", report)
	}

	if len(write.projectHooks[11]) != 0 {
		t.Error("expected no hook on a project a group hook could never reach")
	}
}

func TestSyncHooks_SkipsProjectsWithNoNamespaceReported(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{projects: []*gitlab.Project{{ID: 10}}}
	write := &fakeHookManager{licenseErr: gitlab.ErrNotFound}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Registering a hook is a write; a project whose namespace GitLab didn't
	// report is left alone rather than assumed group-owned.
	if len(write.projectHooks) != 0 {
		t.Errorf("expected no hook on a project with no namespace, got %d", len(write.projectHooks))
	}
}

func TestSyncHooks_StopsWhenProjectsCannotBeListed(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{projectsErr: errors.New("gitlab unreachable")}
	write := &fakeHookManager{licenseErr: gitlab.ErrNotFound}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, nil)
	if err == nil {
		t.Fatal("expected an unlistable instance to fail rather than report an empty sweep as success")
	}
}

func TestSyncHooks_ProjectSweepWalksInAscendingIDOrder(t *testing.T) {
	conn := testConn(t)
	read := &fakeTargetLister{projects: []*gitlab.Project{groupProject(10)}}
	write := &fakeHookManager{licenseErr: gitlab.ErrNotFound}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	opt := read.projectsOpt.ListOptions
	if opt.OrderBy != "id" || opt.Sort != "asc" || opt.Pagination != keysetPagination {
		t.Errorf("expected a keyset walk in ascending id order, matching the backfill's, got %+v", opt)
	}
}
