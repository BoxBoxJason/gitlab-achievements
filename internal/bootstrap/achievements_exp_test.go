package bootstrap

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// expEntry is a one-entry catalog with an EXP reward, the shape these
// tests vary between sync runs.
func expEntry(threshold, exp int64) []catalog.Entry {
	return []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: threshold, Exp: exp, Name: "Committer I", Description: "Made 10 commits."},
	}
}

// awardTo records that a user holds the only seeded definition, the way the
// engine would, with the stale EXP total the old catalog gave them.
func awardTo(t *testing.T, conn *gorm.DB, username string, gitlabUserID, expTotal int64) appdb.User {
	t.Helper()

	user := appdb.User{GitLabUserID: gitlabUserID, Username: username, ExpTotal: expTotal}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	var def appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&def).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	award := appdb.Award{
		UserID:                  user.ID,
		AchievementDefinitionID: def.ID,
		Status:                  appdb.AwardStatusAccepted,
		AwardedAt:               time.Now().UTC(),
	}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}

	return user
}

func expTotalOf(t *testing.T, conn *gorm.DB, userID int64) int64 {
	t.Helper()

	var user appdb.User
	if err := conn.First(&user, userID).Error; err != nil {
		t.Fatalf("failed to reload user %d: %v", userID, err)
	}

	return user.ExpTotal
}

func TestCreateAchievement_PersistsExpReward(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 7)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var def appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&def).Error; err != nil {
		t.Fatalf("failed to load persisted definition: %v", err)
	}

	if def.ExpReward != 7 {
		t.Errorf("expected the tier's 7 EXP to be persisted, got %d", def.ExpReward)
	}
}

func TestReconcileAchievements_ExpOnlyChangeSkipsGitLabCall(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 7)); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 70))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Updated != 1 {
		t.Fatalf("expected the retuned reward to count as an update, got %+v", report)
	}

	// GitLab stores no EXP, so there is nothing to push: the whole change
	// is local.
	if write.updateCalls != 0 {
		t.Errorf("expected no GitLab-side update for a local-only EXP change, got %d calls", write.updateCalls)
	}

	var def appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&def).Error; err != nil {
		t.Fatalf("failed to load persisted definition: %v", err)
	}

	if def.ExpReward != 70 {
		t.Errorf("expected the persisted reward to be updated to 70, got %d", def.ExpReward)
	}
}

func TestRepairExpTotals_RebuildsHoldersAfterARetune(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 7)); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	user := awardTo(t, conn, "alice", 100, 7)

	// The retune pays more for a tier alice already holds. She earns
	// nothing new, so only the sweep can bring her total along.
	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 70))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	corrected, err := repairExpTotals(t.Context(), conn, report)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if corrected != 1 {
		t.Errorf("expected 1 holder's total to be corrected, got %d", corrected)
	}

	if got := expTotalOf(t, conn, user.ID); got != 70 {
		t.Errorf("expected alice's total to follow the retune to 70, got %d", got)
	}
}

func TestRepairExpTotals_BuildsTotalsOnTheUpgradeThatIntroducedExp(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	// What the schema migration leaves behind on an instance that has been
	// awarding since before EXP existed: definitions worth nothing, awards
	// against them, and holders sitting at zero.
	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 0)); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	user := awardTo(t, conn, "alice", 100, 0)

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 7))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	corrected, err := repairExpTotals(t.Context(), conn, report)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if corrected != 1 {
		t.Errorf("expected the upgrade to build 1 holder's total, got %d", corrected)
	}

	if got := expTotalOf(t, conn, user.ID); got != 7 {
		t.Errorf("expected alice to be paid 7 EXP for the tier she already held, got %d", got)
	}
}

func TestRepairExpTotals_SkipsTheSweepWhenNothingChanged(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 7)); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	// A total that is wrong for reasons the catalog knows nothing about is
	// deliberately left alone: the sweep is the retune's repair, not a
	// periodic audit of every user on the instance.
	user := awardTo(t, conn, "alice", 100, 999)

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", expEntry(10, 7))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Unchanged != 1 {
		t.Fatalf("expected the repeat run to change nothing, got %+v", report)
	}

	corrected, err := repairExpTotals(t.Context(), conn, report)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if corrected != 0 {
		t.Errorf("expected no sweep on an unchanged catalog, got %d corrections", corrected)
	}

	if got := expTotalOf(t, conn, user.ID); got != 999 {
		t.Errorf("expected the total to be left untouched, got %d", got)
	}
}

func TestReconcileAchievements_RecreatedEntryKeepsItsExpReward(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := expEntry(10, 7)
	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	var seeded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	// Someone deletes the achievement on GitLab. Reconciliation recreates
	// it under a new ID, rewriting the row in place, which must not drop
	// the reward the holders' totals are derived from.
	delete(write.achievements, seeded.GitLabAchievementID)

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Recreated != 1 {
		t.Fatalf("expected the deleted achievement to be recreated, got %+v", report)
	}

	var recreated appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&recreated).Error; err != nil {
		t.Fatalf("failed to load recreated definition: %v", err)
	}

	if recreated.ExpReward != 7 {
		t.Errorf("expected the recreated definition to keep its 7 EXP, got %d", recreated.ExpReward)
	}
}
