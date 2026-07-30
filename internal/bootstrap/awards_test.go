package bootstrap

import (
	"errors"
	"testing"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

func TestReconcileAwards_ConfirmsPendingAward(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := appdb.User{Username: "alice", GitLabUserID: 100}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	def := appdb.AchievementDefinition{GitLabAchievementID: 9, CriteriaKey: "commits", Tier: 1, Name: "Committer I", Threshold: 10}
	if err := conn.Create(&def).Error; err != nil {
		t.Fatalf("failed to seed achievement definition: %v", err)
	}

	award := appdb.Award{UserID: user.ID, AchievementDefinitionID: def.ID, Status: appdb.AwardStatusPending}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}

	report, err := ReconcileAwards(write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 1 || report.Failed != 0 {
		t.Fatalf("expected 1 confirmed, got %+v", report)
	}

	if write.awardCalls != 1 {
		t.Errorf("expected AwardAchievement to be called once, got %d", write.awardCalls)
	}

	var reloaded appdb.Award
	if err := conn.First(&reloaded, award.ID).Error; err != nil {
		t.Fatalf("failed to reload award: %v", err)
	}

	if reloaded.Status != appdb.AwardStatusAccepted {
		t.Errorf("expected status %q, got %q", appdb.AwardStatusAccepted, reloaded.Status)
	}
}

func TestReconcileAwards_MarksFailedOnRejection(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{awardErr: errors.New("422 unprocessable")}

	user := appdb.User{Username: "bob", GitLabUserID: 101}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	def := appdb.AchievementDefinition{GitLabAchievementID: 9, CriteriaKey: "commits", Tier: 1, Name: "Committer I", Threshold: 10}
	if err := conn.Create(&def).Error; err != nil {
		t.Fatalf("failed to seed achievement definition: %v", err)
	}

	award := appdb.Award{UserID: user.ID, AchievementDefinitionID: def.ID, Status: appdb.AwardStatusPending}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}

	report, err := ReconcileAwards(write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Failed != 1 || report.Confirmed != 0 {
		t.Fatalf("expected 1 failed, got %+v", report)
	}

	var reloaded appdb.Award
	if err := conn.First(&reloaded, award.ID).Error; err != nil {
		t.Fatalf("failed to reload award: %v", err)
	}

	if reloaded.Status != appdb.AwardStatusFailed {
		t.Errorf("expected status %q, got %q", appdb.AwardStatusFailed, reloaded.Status)
	}
}

func TestReconcileAwards_RetriesPreviouslyFailedAward(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := appdb.User{Username: "carol", GitLabUserID: 102}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	def := appdb.AchievementDefinition{GitLabAchievementID: 9, CriteriaKey: "commits", Tier: 1, Name: "Committer I", Threshold: 10}
	if err := conn.Create(&def).Error; err != nil {
		t.Fatalf("failed to seed achievement definition: %v", err)
	}

	award := appdb.Award{UserID: user.ID, AchievementDefinitionID: def.ID, Status: appdb.AwardStatusFailed}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}

	report, err := ReconcileAwards(write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 1 {
		t.Fatalf("expected the previously-failed award to be retried and confirmed, got %+v", report)
	}
}

func TestReconcileAwards_IgnoresAlreadyAcceptedAwards(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	user := appdb.User{Username: "dave", GitLabUserID: 103}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	def := appdb.AchievementDefinition{GitLabAchievementID: 9, CriteriaKey: "commits", Tier: 1, Name: "Committer I", Threshold: 10}
	if err := conn.Create(&def).Error; err != nil {
		t.Fatalf("failed to seed achievement definition: %v", err)
	}

	award := appdb.Award{UserID: user.ID, AchievementDefinitionID: def.ID, Status: appdb.AwardStatusAccepted}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}

	report, err := ReconcileAwards(write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 0 || report.Failed != 0 || write.awardCalls != 0 {
		t.Errorf("expected already-accepted awards to be left untouched, got %+v awardCalls=%d", report, write.awardCalls)
	}
}
