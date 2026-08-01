package bootstrap

import (
	"net/http"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// cleanupCfg is the configuration an achievement removal pass reads: the
// namespace to sweep, and the rate its sweep is paced at.
func cleanupCfg() *config.Config {
	return &config.Config{AchievementsNamespace: "achievements", HookRate: config.DefaultHookRate}
}

// recordedDefinitions counts what the app still believes GitLab holds.
func recordedDefinitions(t *testing.T, conn *gorm.DB) int64 {
	t.Helper()

	var count int64

	err := conn.Model(&appdb.AchievementDefinition{}).Count(&count).Error
	if err != nil {
		t.Fatalf("failed to count achievement definitions: %v", err)
	}

	return count
}

// createdAchievements installs a two-tier catalog the way bootstrap does, so
// cleanup tests start from the state a real deployment leaves behind.
func createdAchievements(t *testing.T) (*gorm.DB, *fakeAchievementWriter) {
	t.Helper()

	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
		{CriteriaKey: "commits", Tier: 2, Threshold: 100, Name: "Committer II", Description: "Made 100 commits."},
	}

	if _, err := syncAchievements(t.Context(), write, conn, 42, entries); err != nil {
		t.Fatalf("failed to install achievements for the cleanup test: %v", err)
	}

	return conn, write
}

func TestRemoveAchievements_DeletesEveryRecordedAchievement(t *testing.T) {
	conn, write := createdAchievements(t)

	if len(write.achievements) != 2 {
		t.Fatalf("expected two achievements to have been created, got %d", len(write.achievements))
	}

	report, err := RemoveAchievements(t.Context(), write, conn, cleanupCfg(), CleanupOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 2 {
		t.Errorf("expected two achievements deleted, got %+v", report)
	}

	if len(write.achievements) != 0 {
		t.Errorf("expected gitlab to hold no achievements, got %d", len(write.achievements))
	}

	if recordedDefinitions(t, conn) != 0 {
		t.Errorf("expected the records to be dropped, got %d", recordedDefinitions(t, conn))
	}
}

func TestRemoveAchievements_CountsOnesGitLabNoLongerHas(t *testing.T) {
	conn, write := createdAchievements(t)

	// Somebody deleted one in GitLab's UI, which is the outcome this pass
	// is reaching for anyway.
	for id := range write.achievements {
		delete(write.achievements, id)

		break
	}

	report, err := RemoveAchievements(t.Context(), write, conn, cleanupCfg(), CleanupOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 1 || report.AlreadyGone != 1 {
		t.Errorf("expected one deleted and one already gone, got %+v", report)
	}

	if recordedDefinitions(t, conn) != 0 {
		t.Errorf("expected both records to be dropped, got %d", recordedDefinitions(t, conn))
	}
}

func TestRemoveAchievements_KeepsWhatItMayNotDelete(t *testing.T) {
	conn, write := createdAchievements(t)

	var refused int64
	for id := range write.achievements {
		refused = id

		break
	}

	write.deleteErrs = map[int64]error{refused: statusErr(http.StatusForbidden)}

	report, err := RemoveAchievements(t.Context(), write, conn, cleanupCfg(), CleanupOptions{})
	if err != nil {
		t.Fatalf("expected a refused deletion not to fail the pass, got: %v", err)
	}

	if report.Deleted != 1 || report.Skipped != 1 {
		t.Errorf("expected one deleted and one skipped, got %+v", report)
	}

	// The record survives, so re-running with a better-privileged token
	// still knows what is left.
	if recordedDefinitions(t, conn) != 1 {
		t.Errorf("expected the skipped achievement's record to be kept, got %d", recordedDefinitions(t, conn))
	}
}

func TestRemoveAchievements_DryRunRemovesNothing(t *testing.T) {
	conn, write := createdAchievements(t)

	report, err := RemoveAchievements(t.Context(), write, conn, cleanupCfg(), CleanupOptions{DryRun: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 2 {
		t.Errorf("expected a dry run to report what it would delete, got %+v", report)
	}

	if write.deleteCalls != 0 {
		t.Errorf("expected a dry run to call gitlab zero times, got %d", write.deleteCalls)
	}

	if len(write.achievements) != 2 || recordedDefinitions(t, conn) != 2 {
		t.Error("expected a dry run to leave both gitlab and the records untouched")
	}
}

func TestRemoveAchievements_SweepTakesUnrecordedCatalogAchievements(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{achievements: map[int64]*gitlab.Achievement{}}

	// An instance whose database was lost: the achievements are in the
	// namespace, but nothing recorded them. One of them was made by hand
	// and is not this app's to delete.
	mine := catalog.V1()[0].Name
	write.achievements[1] = &gitlab.Achievement{ID: 1, Name: mine}
	write.achievements[2] = &gitlab.Achievement{ID: 2, Name: "Employee of the Month"}

	report, err := RemoveAchievements(t.Context(), write, conn, cleanupCfg(), CleanupOptions{Sweep: true})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Swept != 2 {
		t.Errorf("expected the sweep to enumerate both achievements, got %+v", report)
	}

	if report.Deleted != 1 {
		t.Errorf("expected only the catalog achievement to be deleted, got %+v", report)
	}

	if _, held := write.achievements[2]; !held {
		t.Error("expected an achievement this app never created to be left alone")
	}
}

func TestRemoveAchievements_WithoutSweepIgnoresUnrecordedAchievements(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{achievements: map[int64]*gitlab.Achievement{
		1: {ID: 1, Name: catalog.V1()[0].Name},
	}}

	report, err := RemoveAchievements(t.Context(), write, conn, cleanupCfg(), CleanupOptions{})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Deleted != 0 || report.Swept != 0 {
		t.Errorf("expected nothing to be touched without --sweep, got %+v", report)
	}

	if len(write.achievements) != 1 {
		t.Error("expected the unrecorded achievement to survive a pass that was not asked to sweep")
	}
}
