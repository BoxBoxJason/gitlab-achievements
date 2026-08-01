package bootstrap

import (
	"net/http"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// registeredHooks counts what the app still believes it has on GitLab.
func registeredHooks(t *testing.T, conn *gorm.DB) int64 {
	t.Helper()

	var count int64

	err := conn.Model(&appdb.RegisteredHook{}).Count(&count).Error
	if err != nil {
		t.Fatalf("failed to count registered hooks: %v", err)
	}

	return count
}

// totalHooks counts what GitLab actually still has, across both scopes.
func totalHooks(write *fakeHookManager) int {
	total := 0
	for _, hooks := range write.groupHooks {
		total += len(hooks)
	}

	for _, hooks := range write.projectHooks {
		total += len(hooks)
	}

	return total
}

// installed runs a registration sweep, so cleanup tests start from the
// state a real deployment leaves behind rather than a hand-built one.
func installed(t *testing.T, scope config.HookScope, license *gitlab.License) (*gorm.DB, *fakeTargetLister, *fakeHookManager) {
	t.Helper()

	conn := testConn(t)
	read := twoGroupInstance()
	write := &fakeHookManager{license: license}

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(scope), testWebhookURL)
	if err != nil {
		t.Fatalf("failed to install hooks for the cleanup test: %v", err)
	}

	// Forget what installing enumerated, so a test asserting on what
	// cleanup enumerates sees only cleanup's own listings.
	read.groupsOpt = gitlab.ListGroupsOptions{}
	read.projectsOpt = gitlab.ListProjectsOptions{}

	return conn, read, write
}

func TestRemoveWebhooks_DeletesEveryRecordedHook(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, nil)

	if totalHooks(write) != 3 {
		t.Fatalf("expected three project hooks to have been installed, got %d", totalHooks(write))
	}

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, CleanupOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 3 || report.Skipped != 0 || report.AlreadyGone != 0 {
		t.Errorf("expected all three hooks removed, got %+v", report)
	}

	if totalHooks(write) != 0 {
		t.Errorf("expected gitlab to hold no hooks, got %d", totalHooks(write))
	}

	if registeredHooks(t, conn) != 0 {
		t.Errorf("expected no hook records to survive, got %d", registeredHooks(t, conn))
	}

	// Nothing was enumerated: the records name every target directly, which
	// is what keeps an uninstall from costing a full instance walk.
	if report.Swept != 0 || read.projectsOpt.Simple != nil {
		t.Errorf("expected removal to work off the records alone, got %+v", report)
	}
}

func TestRemoveWebhooks_RemovesGroupHooksRecordedUnderTheGroupScope(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeGroup, &gitlab.License{Plan: "ultimate"})

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeGroup), testWebhookURL, CleanupOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 2 {
		t.Errorf("expected one hook per top-level group to be removed, got %+v", report)
	}

	if totalHooks(write) != 0 {
		t.Errorf("expected gitlab to hold no hooks, got %d", totalHooks(write))
	}
}

// A deployment that ran under one scope and was later reconfigured leaves
// records under both. Cleanup reads the scope off each record rather than
// resolving one for the whole pass, so both are removed in a single run.
func TestRemoveWebhooks_RemovesHooksLeftBehindByAnEarlierScope(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, &gitlab.License{Plan: "premium"})

	_, err := syncHooks(t.Context(), read, write, conn, hookCfg(config.HookScopeGroup), testWebhookURL)
	if err != nil {
		t.Fatalf("failed to switch the deployment to group hooks: %v", err)
	}

	if totalHooks(write) != 5 {
		t.Fatalf("expected three project hooks and two group ones, got %d", totalHooks(write))
	}

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeGroup), testWebhookURL, CleanupOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 5 {
		t.Errorf("expected both scopes' hooks to be removed, got %+v", report)
	}

	if totalHooks(write) != 0 {
		t.Errorf("expected gitlab to hold no hooks, got %d", totalHooks(write))
	}
}

// A hook somebody already deleted by hand is the outcome cleanup wants, so
// it counts as gone rather than failing the pass, and its record still goes.
func TestRemoveWebhooks_TreatsAnAlreadyDeletedHookAsRemoved(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, nil)

	write.projectHooks[10] = nil

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, CleanupOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 2 || report.AlreadyGone != 1 {
		t.Errorf("expected the hand-deleted hook to count as already gone, got %+v", report)
	}

	if registeredHooks(t, conn) != 0 {
		t.Errorf("expected no hook records to survive, got %d", registeredHooks(t, conn))
	}
}

// A target the write token may not manage is left alone with its record
// intact, so a re-run with a better token knows exactly where to look, and
// one such target does not cost the rest of the instance its cleanup.
func TestRemoveWebhooks_KeepsTheRecordOfAHookItMayNotRemove(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, nil)

	write.targetDeleteErr = map[int64]error{11: statusErr(http.StatusForbidden)}

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, CleanupOptions{})
	if err != nil {
		t.Fatalf("expected a forbidden target not to fail the pass, got: %v", err)
	}

	if report.Deleted != 2 || report.Skipped != 1 {
		t.Errorf("expected the forbidden target to be skipped and the rest removed, got %+v", report)
	}

	if registeredHooks(t, conn) != 1 {
		t.Errorf("expected the skipped hook's record to be kept, got %d", registeredHooks(t, conn))
	}

	if len(write.projectHooks[11]) != 1 {
		t.Errorf("expected the hook this app may not remove to still be on gitlab")
	}
}

// Anything that isn't target-local stops the pass: a GitLab-wide outage
// must not be reported as every target having been quietly skipped.
func TestRemoveWebhooks_StopsOnAFailureThatIsNotTargetLocal(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, nil)

	write.deleteErr = statusErr(http.StatusInternalServerError)

	_, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, CleanupOptions{})
	if err == nil {
		t.Fatal("expected a server-side failure to fail the pass")
	}

	if registeredHooks(t, conn) != 3 {
		t.Errorf("expected every record to be kept when nothing could be removed, got %d", registeredHooks(t, conn))
	}
}

func TestRemoveWebhooks_DryRunRemovesNothing(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, nil)

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, CleanupOptions{DryRun: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 3 {
		t.Errorf("expected a dry run to report what it would remove, got %+v", report)
	}

	if write.deleteCalls != 0 {
		t.Errorf("expected a dry run not to call gitlab, got %d delete calls", write.deleteCalls)
	}

	if totalHooks(write) != 3 || registeredHooks(t, conn) != 3 {
		t.Errorf("expected a dry run to leave hooks and records alone, got %d hooks and %d records",
			totalHooks(write), registeredHooks(t, conn))
	}
}

// The sweep is what covers a database that no longer knows what was
// registered: the hooks are still on GitLab and are recognized by their URL.
func TestRemoveWebhooks_SweepRemovesHooksWithNoRecord(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, nil)

	err := conn.Where("1 = 1").Delete(&appdb.RegisteredHook{}).Error
	if err != nil {
		t.Fatalf("failed to simulate a lost database: %v", err)
	}

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, CleanupOptions{Sweep: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 3 {
		t.Errorf("expected the sweep to find all three hooks by url, got %+v", report)
	}

	if report.Swept != 3 {
		t.Errorf("expected all three projects to be enumerated, got %+v", report)
	}

	if totalHooks(write) != 0 {
		t.Errorf("expected gitlab to hold no hooks, got %d", totalHooks(write))
	}
}

// The sweep must not touch hooks belonging to anything else on the
// instance, which is the whole reason it matches on this app's URL.
func TestRemoveWebhooks_SweepLeavesForeignHooksAlone(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, nil)

	foreign := &gitlab.ProjectHook{ID: 999, URL: "https://ci.example.com/hook", ProjectID: 10}
	write.projectHooks[10] = append(write.projectHooks[10], foreign)

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeProject), testWebhookURL, CleanupOptions{Sweep: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 3 {
		t.Errorf("expected only this app's hooks to be removed, got %+v", report)
	}

	if len(write.projectHooks[10]) != 1 || write.projectHooks[10][0].ID != 999 {
		t.Errorf("expected another integration's hook to survive, got %+v", write.projectHooks[10])
	}
}

// Under "auto" an instance that supports group hooks is swept for project
// hooks too: it may have been running the project scope before its license
// changed, and those hooks would otherwise be left behind forever.
func TestRemoveWebhooks_SweepCoversBothScopesUnderAuto(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, &gitlab.License{Plan: "premium"})

	err := conn.Where("1 = 1").Delete(&appdb.RegisteredHook{}).Error
	if err != nil {
		t.Fatalf("failed to simulate a lost database: %v", err)
	}

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, CleanupOptions{Sweep: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(report.SweptScopes) != 2 {
		t.Errorf("expected auto to sweep both scopes on a paid instance, got %v", report.SweptScopes)
	}

	if report.Deleted != 3 || totalHooks(write) != 0 {
		t.Errorf("expected the project hooks a paid instance was left with to be removed, got %+v", report)
	}
}

// A free instance has never had a group hook registered on it, so sweeping
// for them is a pointless walk of every group on the instance.
func TestRemoveWebhooks_SweepSkipsGroupsWhereGroupHooksCannotExist(t *testing.T) {
	conn, read, write := installed(t, config.HookScopeProject, nil)

	report, err := RemoveWebhooks(t.Context(), read, write, conn, hookCfg(config.HookScopeAuto), testWebhookURL, CleanupOptions{Sweep: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(report.SweptScopes) != 1 || report.SweptScopes[0] != appdb.HookScopeProject {
		t.Errorf("expected only the project scope to be swept, got %v", report.SweptScopes)
	}

	if read.groupsOpt.TopLevelOnly != nil {
		t.Error("expected no group enumeration on an instance without group hooks")
	}
}
