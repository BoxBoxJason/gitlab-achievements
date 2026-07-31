package bootstrap

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// seedUser creates a local user row and tells write which GitLab user ID
// that username answers to, so awards the fake "already holds" can be
// listed back for them.
func seedUser(t *testing.T, conn *gorm.DB, write *fakeAchievementWriter, username string, gitlabUserID int64) appdb.User {
	t.Helper()

	user := appdb.User{Username: username, GitLabUserID: gitlabUserID}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	if write.gitlabUserIDs == nil {
		write.gitlabUserIDs = make(map[string]int64)
	}

	write.gitlabUserIDs[username] = gitlabUserID

	return user
}

// seedTier creates the achievement definition for one tier of a criteria.
func seedTier(t *testing.T, conn *gorm.DB, criteriaKey string, tier, gitlabAchievementID int64) appdb.AchievementDefinition {
	t.Helper()

	def := appdb.AchievementDefinition{
		GitLabAchievementID: gitlabAchievementID,
		CriteriaKey:         criteriaKey,
		Tier:                tier,
		Name:                "Committer",
		Threshold:           tier * 10,
	}
	if err := conn.Create(&def).Error; err != nil {
		t.Fatalf("failed to seed achievement definition: %v", err)
	}

	return def
}

func seedAward(t *testing.T, conn *gorm.DB, user appdb.User, def appdb.AchievementDefinition, status appdb.AwardStatus) appdb.Award {
	t.Helper()

	award := appdb.Award{UserID: user.ID, AchievementDefinitionID: def.ID, Status: status}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}

	return award
}

func reloadAward(t *testing.T, conn *gorm.DB, id int64) appdb.Award {
	t.Helper()

	var award appdb.Award
	if err := conn.First(&award, id).Error; err != nil {
		t.Fatalf("failed to reload award: %v", err)
	}

	return award
}

func TestReconcileAwards_ConfirmsPendingAward(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "alice", 100)
	def := seedTier(t, conn, "commits", 1, 9)
	award := seedAward(t, conn, user, def, appdb.AwardStatusPending)

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 1 || report.Failed != 0 {
		t.Fatalf("expected 1 confirmed, got %+v", report)
	}

	if write.awardCalls != 1 {
		t.Errorf("expected AwardAchievement to be called once, got %d", write.awardCalls)
	}

	reloaded := reloadAward(t, conn, award.ID)

	if reloaded.Status != appdb.AwardStatusAccepted {
		t.Errorf("expected status %q, got %q", appdb.AwardStatusAccepted, reloaded.Status)
	}

	if reloaded.GitLabUserAchievementID == 0 {
		t.Error("expected the GitLab user-achievement ID to be recorded, got 0")
	}

	if reloaded.ShownOnProfile {
		t.Error("expected a fresh award to be unaccepted, since only its recipient can accept it")
	}
}

func TestReconcileAwards_MarksFailedOnRejection(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{awardErr: errors.New("422 unprocessable")}

	user := seedUser(t, conn, write, "bob", 101)
	def := seedTier(t, conn, "commits", 1, 9)
	award := seedAward(t, conn, user, def, appdb.AwardStatusPending)

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Failed != 1 || report.Confirmed != 0 {
		t.Fatalf("expected 1 failed, got %+v", report)
	}

	if reloadAward(t, conn, award.ID).Status != appdb.AwardStatusFailed {
		t.Errorf("expected status %q", appdb.AwardStatusFailed)
	}
}

func TestReconcileAwards_RetriesPreviouslyFailedAward(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "carol", 102)
	def := seedTier(t, conn, "commits", 1, 9)
	seedAward(t, conn, user, def, appdb.AwardStatusFailed)

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 1 {
		t.Fatalf("expected the previously-failed award to be retried and confirmed, got %+v", report)
	}
}

func TestReconcileAwards_LeavesDeliveredAwardsAlone(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "dave", 103)
	def := seedTier(t, conn, "commits", 1, 9)

	award := seedAward(t, conn, user, def, appdb.AwardStatusAccepted)
	if err := conn.Model(&award).Update("git_lab_user_achievement_id", 77).Error; err != nil {
		t.Fatalf("failed to seed the delivered award's GitLab ID: %v", err)
	}

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report != (AwardsReport{}) || write.awardCalls != 0 {
		t.Errorf("expected a delivered award to be left untouched, got %+v awardCalls=%d", report, write.awardCalls)
	}
}

// TestReconcileAwards_AwardsOnlyTheTopTier covers the rule the whole
// delivery path exists for: the engine records every tier a user reaches,
// but GitLab is only ever told about the highest one, so a backfill over
// years of history costs one award (and one notification email) per
// criteria rather than one per tier.
func TestReconcileAwards_AwardsOnlyTheTopTier(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "erin", 104)

	tierOne := seedTier(t, conn, "commits", 1, 11)
	tierTwo := seedTier(t, conn, "commits", 2, 12)
	tierThree := seedTier(t, conn, "commits", 3, 13)

	lower := seedAward(t, conn, user, tierOne, appdb.AwardStatusPending)
	middle := seedAward(t, conn, user, tierTwo, appdb.AwardStatusPending)
	top := seedAward(t, conn, user, tierThree, appdb.AwardStatusPending)

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 1 || report.Superseded != 2 {
		t.Fatalf("expected 1 confirmed and 2 superseded, got %+v", report)
	}

	if write.awardCalls != 1 {
		t.Errorf("expected exactly one award call, got %d", write.awardCalls)
	}

	if write.revokeCalls != 0 {
		t.Errorf("expected no revoke calls for tiers GitLab never held, got %d", write.revokeCalls)
	}

	if got := reloadAward(t, conn, top.ID).Status; got != appdb.AwardStatusAccepted {
		t.Errorf("expected the top tier to be delivered, got %q", got)
	}

	for _, superseded := range []appdb.Award{lower, middle} {
		reloaded := reloadAward(t, conn, superseded.ID)

		if reloaded.Status != appdb.AwardStatusSuperseded {
			t.Errorf("expected the lower tier to be superseded, got %q", reloaded.Status)
		}

		if reloaded.GitLabUserAchievementID != 0 {
			t.Errorf("expected a superseded tier never pushed to GitLab to hold no ID, got %d", reloaded.GitLabUserAchievementID)
		}
	}
}

// TestReconcileAwards_RevokesTierASupersedingOneReplaces covers the
// promotion case: a tier GitLab already holds has to come back off once its
// holder reaches a higher one, which revoking is the only mutation this
// app's token is allowed to make on someone else's award.
func TestReconcileAwards_RevokesTierASupersedingOneReplaces(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "frank", 105)
	tierOne := seedTier(t, conn, "commits", 1, 11)
	tierTwo := seedTier(t, conn, "commits", 2, 12)

	held := write.grantAward(tierOne.GitLabAchievementID, user.GitLabUserID)

	delivered := seedAward(t, conn, user, tierOne, appdb.AwardStatusAccepted)
	if err := conn.Model(&delivered).Update("git_lab_user_achievement_id", held.ID).Error; err != nil {
		t.Fatalf("failed to seed the delivered award's GitLab ID: %v", err)
	}

	promoted := seedAward(t, conn, user, tierTwo, appdb.AwardStatusPending)

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 1 || report.Superseded != 1 {
		t.Fatalf("expected the higher tier delivered and the lower one superseded, got %+v", report)
	}

	if write.revokeCalls != 1 {
		t.Errorf("expected the superseded tier to be revoked once, got %d", write.revokeCalls)
	}

	if held.RevokedAt == nil {
		t.Error("expected the superseded tier to be revoked on GitLab")
	}

	if got := reloadAward(t, conn, delivered.ID).Status; got != appdb.AwardStatusSuperseded {
		t.Errorf("expected the replaced tier to be superseded, got %q", got)
	}

	if got := reloadAward(t, conn, promoted.ID).Status; got != appdb.AwardStatusAccepted {
		t.Errorf("expected the new top tier to be delivered, got %q", got)
	}
}

// TestReconcileAwards_AdoptsAwardGitLabAlreadyHolds is the duplicate-award
// regression guard. Awarding is not idempotent on GitLab's side, so an
// award delivered but never recorded locally must be matched back to
// GitLab's own record rather than awarded again, which would leave the
// recipient holding the same achievement twice and emailed about it twice.
func TestReconcileAwards_AdoptsAwardGitLabAlreadyHolds(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "grace", 106)
	def := seedTier(t, conn, "commits", 1, 9)

	held := write.grantAward(def.GitLabAchievementID, user.GitLabUserID)
	held.ShowOnProfile = true

	award := seedAward(t, conn, user, def, appdb.AwardStatusAccepted)

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Adopted != 1 || write.awardCalls != 0 {
		t.Fatalf("expected the existing award to be adopted rather than re-awarded, got %+v awardCalls=%d", report, write.awardCalls)
	}

	reloaded := reloadAward(t, conn, award.ID)

	if reloaded.GitLabUserAchievementID != held.ID {
		t.Errorf("expected the GitLab ID %d to be recovered, got %d", held.ID, reloaded.GitLabUserAchievementID)
	}

	if !reloaded.ShownOnProfile {
		t.Error("expected the recipient's acceptance to be read back from GitLab")
	}
}

// TestReconcileAwards_DoesNotReawardOnASecondPass guards the same
// duplicate-award hazard across reconciliation passes, which is how it
// would show up in production: an hourly sweep must not re-push what the
// last one already delivered.
func TestReconcileAwards_DoesNotReawardOnASecondPass(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "heidi", 107)
	def := seedTier(t, conn, "commits", 1, 9)
	seedAward(t, conn, user, def, appdb.AwardStatusPending)

	for pass := range 3 {
		if _, err := ReconcileAwards(t.Context(), write, conn); err != nil {
			t.Fatalf("pass %d: expected no error, got: %v", pass, err)
		}
	}

	if write.awardCalls != 1 {
		t.Errorf("expected the award to be pushed exactly once across three passes, got %d", write.awardCalls)
	}
}

// TestReconcileAwards_SupersessionIsPerCriteria makes sure the top-tier
// rule is scoped to one stack: reaching Committer III says nothing about
// which Reviewer tier should be on the profile.
func TestReconcileAwards_SupersessionIsPerCriteria(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "ivan", 108)

	commits := seedTier(t, conn, "commits", 2, 21)
	reviews := seedTier(t, conn, "merge_requests_approved", 1, 31)

	seedAward(t, conn, user, commits, appdb.AwardStatusPending)
	seedAward(t, conn, user, reviews, appdb.AwardStatusPending)

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 2 || report.Superseded != 0 {
		t.Fatalf("expected both criteria's top tiers delivered, got %+v", report)
	}
}

// TestReconcileAwards_RedeliversATierThatBecomesTopAgain covers the case a
// catalog retune can create: dropping a criteria's top tiers renumbers the
// stack in place, so a tier this app already withdrew can end up the
// highest one a user holds. Supersession has to be a decision each pass
// re-makes, not a one-way door.
func TestReconcileAwards_RedeliversATierThatBecomesTopAgain(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "ken", 110)
	tierOne := seedTier(t, conn, "commits", 1, 11)
	tierTwo := seedTier(t, conn, "commits", 2, 12)

	// A stack already reconciled: tier 2 delivered, tier 1 withdrawn.
	revoked := write.grantAward(tierOne.GitLabAchievementID, user.GitLabUserID)
	revokedAt := time.Now()
	revoked.RevokedAt = &revokedAt

	lower := seedAward(t, conn, user, tierOne, appdb.AwardStatusSuperseded)
	if err := conn.Model(&lower).Update("git_lab_user_achievement_id", revoked.ID).Error; err != nil {
		t.Fatalf("failed to seed the withdrawn award's GitLab ID: %v", err)
	}

	upper := seedAward(t, conn, user, tierTwo, appdb.AwardStatusAccepted)
	held := write.grantAward(tierTwo.GitLabAchievementID, user.GitLabUserID)

	if err := conn.Model(&upper).Update("git_lab_user_achievement_id", held.ID).Error; err != nil {
		t.Fatalf("failed to seed the delivered award's GitLab ID: %v", err)
	}

	// The retune drops tier 2, leaving tier 1 the top tier held.
	if err := conn.Delete(&upper).Error; err != nil {
		t.Fatalf("failed to drop the retuned-away award: %v", err)
	}

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 1 {
		t.Fatalf("expected the surviving tier to be delivered again, got %+v", report)
	}

	reloaded := reloadAward(t, conn, lower.ID)

	if reloaded.Status != appdb.AwardStatusAccepted {
		t.Errorf("expected the surviving tier to be delivered, got %q", reloaded.Status)
	}

	if reloaded.GitLabUserAchievementID == revoked.ID {
		t.Error("expected a fresh GitLab award, since a revoked one cannot be un-revoked")
	}
}

func TestReconcileAwards_LeavesSupersededAwardsSettled(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := seedUser(t, conn, write, "judy", 109)
	tierOne := seedTier(t, conn, "commits", 1, 11)
	tierTwo := seedTier(t, conn, "commits", 2, 12)

	seedAward(t, conn, user, tierOne, appdb.AwardStatusSuperseded)
	seedAward(t, conn, user, tierTwo, appdb.AwardStatusPending)

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Superseded != 0 {
		t.Errorf("expected an already-superseded tier not to be counted again, got %+v", report)
	}
}
