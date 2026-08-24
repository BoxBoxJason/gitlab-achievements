package bootstrap

import (
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// forgetDefinitions drops every achievement definition row, leaving what
// GitLab holds untouched. This is the database this app is pointed at
// having been rebuilt from nothing, or restored from a backup taken before
// the achievements it describes were created: the only record of which
// GitLab achievement belongs to which catalog entry is gone.
func forgetDefinitions(t *testing.T, conn *gorm.DB) {
	t.Helper()

	if err := conn.Where("1 = 1").Delete(&appdb.AchievementDefinition{}).Error; err != nil {
		t.Fatalf("failed to clear achievement definitions: %v", err)
	}
}

func TestReconcileAchievements_AdoptsWhatGitLabAlreadyHolds(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
		{CriteriaKey: "commits", Tier: 2, Threshold: 100, Name: "Committer II", Description: "Made 100 commits."},
	}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	var seeded []appdb.AchievementDefinition
	if err := conn.Order("tier").Find(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definitions: %v", err)
	}

	createsWhileSeeding := write.nextID

	forgetDefinitions(t, conn)

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Adopted != 2 || report.Created != 0 || report.Recreated != 0 {
		t.Fatalf("expected both entries adopted rather than created, got %+v", report)
	}

	// Creating them again is not merely wasteful, it is what GitLab refuses
	// outright: an achievement name is unique within a namespace, which is
	// what made this state fatal before adoption.
	if write.nextID != createsWhileSeeding {
		t.Errorf("expected no CreateAchievement call, %d more were made", write.nextID-createsWhileSeeding)
	}

	if len(write.achievements) != 2 {
		t.Errorf("expected GitLab to still hold exactly 2 achievements, got %d", len(write.achievements))
	}

	var readopted []appdb.AchievementDefinition
	if err := conn.Order("tier").Find(&readopted).Error; err != nil {
		t.Fatalf("failed to reload definitions: %v", err)
	}

	if len(readopted) != 2 {
		t.Fatalf("expected 2 rows written back, got %d", len(readopted))
	}

	for i, def := range readopted {
		if def.GitLabAchievementID != seeded[i].GitLabAchievementID {
			t.Errorf("expected %q to be bound back to achievement %d, got %d", def.Name, seeded[i].GitLabAchievementID, def.GitLabAchievementID)
		}

		if def.CriteriaKey != entries[i].CriteriaKey || def.Tier != entries[i].Tier || def.Threshold != entries[i].Threshold {
			t.Errorf("expected the adopted row to carry the catalog's criteria, got %+v", def)
		}
	}
}

func TestReconcileAchievements_AdoptionKeepsAwardsIntact(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	var seeded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	// Somebody earned it before the database was lost. Adoption is what
	// keeps the badge on their profile: recreating the achievement would
	// take every award of it down with the original.
	awarded := write.grantAward(seeded.GitLabAchievementID, 7)

	forgetDefinitions(t, conn)

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Adopted != 1 {
		t.Fatalf("expected 1 adopted, got %+v", report)
	}

	held := write.userAwards[7]
	if len(held) != 1 || held[0].ID != awarded.ID {
		t.Fatalf("expected the existing award to survive adoption untouched, got %+v", held)
	}

	if held[0].AchievementID != seeded.GitLabAchievementID {
		t.Errorf("expected the award to still hang off achievement %d, got %d", seeded.GitLabAchievementID, held[0].AchievementID)
	}
}

func TestReconcileAchievements_AdoptedEntryIsPulledInLine(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	stale := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "An older wording."},
	}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", stale); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	var seeded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	forgetDefinitions(t, conn)

	// The catalog has moved on since the achievement GitLab holds was
	// written. Adopting it is not enough: it has to end the pass matching
	// the catalog, the same as one that was never lost.
	current := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", current)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Adopted != 1 {
		t.Fatalf("expected 1 adopted, got %+v", report)
	}

	if write.updateCalls != 1 {
		t.Errorf("expected the adopted achievement's drift to be pushed once, got %d update calls", write.updateCalls)
	}

	live := write.achievements[seeded.GitLabAchievementID]
	if live == nil || live.Description == nil || *live.Description != "Made 10 commits." {
		t.Fatalf("expected GitLab's description to be brought in line with the catalog, got %+v", live)
	}

	var adopted appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&adopted).Error; err != nil {
		t.Fatalf("failed to reload definition: %v", err)
	}

	if adopted.Description != "Made 10 commits." {
		t.Errorf("expected the row to mirror the catalog after adoption, got %q", adopted.Description)
	}
}

func TestReconcileAchievements_AdoptedEntryGetsTheAvatarItIsMissing(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	withoutAvatar := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", withoutAvatar); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	var seeded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	forgetDefinitions(t, conn)

	withAvatar := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits.", AvatarPath: "assets/commit.png"},
	}

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", withAvatar)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Adopted != 1 {
		t.Fatalf("expected 1 adopted, got %+v", report)
	}

	if write.avatarUploads != 1 {
		t.Errorf("expected the missing avatar to be uploaded once on adoption, got %d", write.avatarUploads)
	}

	if write.achievements[seeded.GitLabAchievementID].AvatarURL == nil {
		t.Error("expected the adopted achievement to have an avatar, still nil")
	}
}

func TestReconcileAchievements_AdoptedEntryKeepsTheAvatarItHas(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits.", AvatarPath: "assets/commit.png"},
	}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	forgetDefinitions(t, conn)

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Adopted != 1 {
		t.Fatalf("expected 1 adopted, got %+v", report)
	}

	// GitLab exposes an avatar's URL but not its bytes, so one already
	// there is taken to be the catalog's. Re-uploading on every adoption
	// would be 275 pointless multipart requests on a rebuilt database.
	if write.avatarUploads != 1 {
		t.Errorf("expected no second avatar upload for an achievement that already has one, got %d total", write.avatarUploads)
	}

	if write.updateCalls != 0 {
		t.Errorf("expected no UpdateAchievement call for an adopted entry already in sync, got %d", write.updateCalls)
	}
}

func TestReconcileAchievements_AdoptsWhenTheRecordedIDWentStale(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	if _, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	var seeded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	onGitLab := seeded.GitLabAchievementID

	// A row pointing at an achievement GitLab does not have, while GitLab
	// holds one under the same name: a database restored from a backup
	// taken before the achievements were last recreated. Recreating here
	// would fail on the duplicate name.
	if err := conn.Model(&seeded).Update("git_lab_achievement_id", onGitLab+1000).Error; err != nil {
		t.Fatalf("failed to stale out the recorded achievement id: %v", err)
	}

	createsWhileSeeding := write.nextID

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Adopted != 1 || report.Recreated != 0 {
		t.Fatalf("expected the stale row to be adopted rather than recreated, got %+v", report)
	}

	if write.nextID != createsWhileSeeding {
		t.Errorf("expected no CreateAchievement call, %d more were made", write.nextID-createsWhileSeeding)
	}

	var rebound appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&rebound).Error; err != nil {
		t.Fatalf("failed to reload definition: %v", err)
	}

	if rebound.GitLabAchievementID != onGitLab {
		t.Errorf("expected the row to be pointed back at achievement %d, got %d", onGitLab, rebound.GitLabAchievementID)
	}

	if rebound.ID != seeded.ID {
		t.Errorf("expected the row to be rebound in place, got a different row id %d vs %d", rebound.ID, seeded.ID)
	}
}

func TestReconcileAchievements_CreatesWhenNoNameMatches(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	// An achievement under a name no catalog entry claims is somebody
	// else's, or an entry the catalog has since renamed. Either way it is
	// not adoptable, and the entry gets one of its own.
	write.nextID++
	other := "Something else entirely."
	write.achievements = map[int64]*gitlab.Achievement{
		write.nextID: {ID: write.nextID, Name: "Hand-made", Description: &other},
	}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created != 1 || report.Adopted != 0 {
		t.Fatalf("expected 1 created and nothing adopted, got %+v", report)
	}

	if len(write.achievements) != 2 {
		t.Errorf("expected the unrelated achievement to be left alone alongside the new one, got %d", len(write.achievements))
	}
}
