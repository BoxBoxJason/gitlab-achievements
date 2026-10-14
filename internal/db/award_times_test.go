package db_test

import (
	"strings"
	"testing"
	"time"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

func TestMigrate_NormalizesSQLiteAwardTimesToUTC(t *testing.T) {
	conn := openMigratedTestDB(t)

	user := db.User{Username: "alice", GitLabUserID: 42}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	definition := db.AchievementDefinition{CriteriaKey: "commits", Name: "Committer I", GitLabAchievementID: 1, Tier: 1, Threshold: 1}
	if err := conn.Create(&definition).Error; err != nil {
		t.Fatalf("failed to seed definition: %v", err)
	}

	// How the engine used to date an award arriving in a +02:00 zone.
	awardedAt := time.Date(2026, time.October, 14, 20, 41, 0, 0, time.FixedZone("", 2*60*60))

	award := db.Award{AwardedAt: awardedAt, UserID: user.ID, AchievementDefinitionID: definition.ID}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}

	if err := db.Migrate(conn); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	var stored string
	if err := conn.Raw("SELECT awarded_at || '' FROM awards WHERE id = ?", award.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("failed to read the stored time: %v", err)
	}

	if !strings.HasSuffix(stored, "+00:00") {
		t.Errorf("expected the award time stored in UTC, got %q", stored)
	}

	var reloaded db.Award
	if err := conn.First(&reloaded, award.ID).Error; err != nil {
		t.Fatalf("failed to reload the award: %v", err)
	}

	if !reloaded.AwardedAt.Equal(awardedAt) {
		t.Errorf("expected the same instant %s after normalizing, got %s", awardedAt, reloaded.AwardedAt)
	}

	// The window starts after the award happened (18:41 UTC), so a text
	// comparison must now agree that the award is outside it.
	var inWindow int64

	since := time.Date(2026, time.October, 14, 19, 0, 0, 0, time.UTC)
	if err := conn.Model(&db.Award{}).Where("awarded_at >= ?", since).Count(&inWindow).Error; err != nil {
		t.Fatalf("failed to count awards in the window: %v", err)
	}

	if inWindow != 0 {
		t.Errorf("expected the 18:41 UTC award outside a window from 19:00 UTC, got %d inside", inWindow)
	}
}
