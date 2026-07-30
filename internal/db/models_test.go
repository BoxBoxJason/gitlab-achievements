package db_test

import (
	"testing"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

func migratedTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	conn := openTestDB(t)

	if err := db.Migrate(conn); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}

	return conn
}

func TestUser_UniqueGitLabUserID(t *testing.T) {
	conn := migratedTestDB(t)

	first := &db.User{GitLabUserID: 42, Username: "alice"}
	if err := conn.Create(first).Error; err != nil {
		t.Fatalf("failed to create first user: %v", err)
	}

	second := &db.User{GitLabUserID: 42, Username: "alice-dup"}

	err := conn.Create(second).Error
	if !isUniqueConstraintErr(err) {
		t.Fatalf("expected a unique constraint error, got: %v", err)
	}
}

func TestAchievementDefinition_UniqueCriteriaTier(t *testing.T) {
	conn := migratedTestDB(t)

	first := &db.AchievementDefinition{
		GitLabAchievementID: 1,
		CriteriaKey:         "merge_requests_merged",
		Tier:                1,
		Threshold:           10,
	}
	if err := conn.Create(first).Error; err != nil {
		t.Fatalf("failed to create first achievement definition: %v", err)
	}

	second := &db.AchievementDefinition{
		GitLabAchievementID: 2,
		CriteriaKey:         "merge_requests_merged",
		Tier:                1,
		Threshold:           20,
	}

	err := conn.Create(second).Error
	if !isUniqueConstraintErr(err) {
		t.Fatalf("expected a unique constraint error, got: %v", err)
	}
}

func TestProgressCounter_UniqueUserCriteria(t *testing.T) {
	conn := migratedTestDB(t)

	user := &db.User{GitLabUserID: 1, Username: "bob"}
	if err := conn.Create(user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	first := &db.ProgressCounter{UserID: user.ID, CriteriaKey: "commits_pushed", Count: 5}
	if err := conn.Create(first).Error; err != nil {
		t.Fatalf("failed to create first progress counter: %v", err)
	}

	second := &db.ProgressCounter{UserID: user.ID, CriteriaKey: "commits_pushed", Count: 1}

	err := conn.Create(second).Error
	if !isUniqueConstraintErr(err) {
		t.Fatalf("expected a unique constraint error, got: %v", err)
	}
}

func TestAward_UniqueUserAchievement(t *testing.T) {
	conn := migratedTestDB(t)

	user := &db.User{GitLabUserID: 2, Username: "carol"}
	if err := conn.Create(user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	definition := &db.AchievementDefinition{
		GitLabAchievementID: 3,
		CriteriaKey:         "issues_closed",
		Tier:                1,
		Threshold:           5,
	}
	if err := conn.Create(definition).Error; err != nil {
		t.Fatalf("failed to create achievement definition: %v", err)
	}

	first := &db.Award{UserID: user.ID, AchievementDefinitionID: definition.ID, Status: db.AwardStatusPending}
	if err := conn.Create(first).Error; err != nil {
		t.Fatalf("failed to create first award: %v", err)
	}

	second := &db.Award{UserID: user.ID, AchievementDefinitionID: definition.ID, Status: db.AwardStatusPending}

	err := conn.Create(second).Error
	if !isUniqueConstraintErr(err) {
		t.Fatalf("expected a unique constraint error, got: %v", err)
	}
}

func TestProcessedEvent_UniqueEventID(t *testing.T) {
	conn := migratedTestDB(t)

	first := &db.ProcessedEvent{EventID: "evt-1", EventType: "push"}
	if err := conn.Create(first).Error; err != nil {
		t.Fatalf("failed to create first processed event: %v", err)
	}

	second := &db.ProcessedEvent{EventID: "evt-1", EventType: "push"}

	err := conn.Create(second).Error
	if !isUniqueConstraintErr(err) {
		t.Fatalf("expected a unique constraint error, got: %v", err)
	}
}

func TestSyncState_UniqueKey(t *testing.T) {
	conn := migratedTestDB(t)

	first := &db.SyncState{Key: "backfill_cursor", Value: "100"}
	if err := conn.Create(first).Error; err != nil {
		t.Fatalf("failed to create first sync state: %v", err)
	}

	second := &db.SyncState{Key: "backfill_cursor", Value: "200"}

	err := conn.Create(second).Error
	if !isUniqueConstraintErr(err) {
		t.Fatalf("expected a unique constraint error, got: %v", err)
	}
}
