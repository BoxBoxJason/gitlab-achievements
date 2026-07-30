package engine

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

func testConn(t *testing.T) *gorm.DB {
	t.Helper()

	conn, err := appdb.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory test database: %v", err)
	}

	if err := appdb.Migrate(conn); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}

	return conn
}

// seedDefinitions inserts achievement definitions the way bootstrap would,
// so the engine has tiers to award against.
func seedDefinitions(t *testing.T, conn *gorm.DB, criteriaKey string, thresholds ...int64) []appdb.AchievementDefinition {
	t.Helper()

	definitions := make([]appdb.AchievementDefinition, 0, len(thresholds))

	for i, threshold := range thresholds {
		tier := int64(i + 1)
		definition := appdb.AchievementDefinition{
			GitLabAchievementID: int64(len(criteriaKey)*100) + tier,
			CriteriaKey:         criteriaKey,
			Name:                criteriaKey,
			Tier:                tier,
			Threshold:           threshold,
		}

		if err := conn.Create(&definition).Error; err != nil {
			t.Fatalf("failed to seed achievement definition: %v", err)
		}

		definitions = append(definitions, definition)
	}

	return definitions
}

func commitEvent(dedupKey string, count int64) activity.Event {
	return activity.Event{
		OccurredAt:    time.Date(2024, time.May, 3, 10, 0, 0, 0, time.UTC),
		Kind:          activity.KindCommit,
		DedupKey:      dedupKey,
		ActorUsername: "alice",
		ActorID:       100,
		ProjectID:     7,
		Count:         count,
	}
}

func counterFor(t *testing.T, conn *gorm.DB, criteriaKey string) int64 {
	t.Helper()

	var counter appdb.ProgressCounter
	if err := conn.Where("criteria_key = ?", criteriaKey).First(&counter).Error; err != nil {
		t.Fatalf("failed to load %q counter: %v", criteriaKey, err)
	}

	return counter.Count
}

func awardCount(t *testing.T, conn *gorm.DB) int64 {
	t.Helper()

	var count int64
	if err := conn.Model(&appdb.Award{}).Count(&count).Error; err != nil {
		t.Fatalf("failed to count awards: %v", err)
	}

	return count
}

func TestProcess_CountsActivityAndCreatesUser(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 3)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got := counterFor(t, conn, catalog.CriteriaCommits); got != 3 {
		t.Errorf("expected the push's 3 commits to be counted, got %d", got)
	}

	var user appdb.User
	if err := conn.Where("git_lab_user_id = ?", 100).First(&user).Error; err != nil {
		t.Fatalf("expected the actor to be created, got: %v", err)
	}

	if user.Username != "alice" {
		t.Errorf("expected username %q, got %q", "alice", user.Username)
	}
}

func TestProcess_IsIdempotentOnReplay(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	event := commitEvent("project_event:1:commit", 5)

	for range 3 {
		if err := eng.Process(t.Context(), event); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	if got := counterFor(t, conn, catalog.CriteriaCommits); got != 5 {
		t.Errorf("expected a replayed event to be counted once (5), got %d", got)
	}

	stats := eng.Stats()
	if stats.Processed != 1 || stats.Skipped != 2 {
		t.Errorf("expected 1 processed and 2 skipped, got %+v", stats)
	}
}

func TestProcess_AwardsEveryTierReached(t *testing.T) {
	conn := testConn(t)
	definitions := seedDefinitions(t, conn, catalog.CriteriaCommits, 10, 100)
	eng := New(conn)

	// A single event carrying a user's whole history crosses both tiers at
	// once; both must be awarded, not just the highest.
	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 150)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var awards []appdb.Award
	if err := conn.Order("achievement_definition_id").Find(&awards).Error; err != nil {
		t.Fatalf("failed to load awards: %v", err)
	}

	if len(awards) != len(definitions) {
		t.Fatalf("expected %d awards, got %d", len(definitions), len(awards))
	}

	for i, award := range awards {
		if award.AchievementDefinitionID != definitions[i].ID {
			t.Errorf("expected award for definition %d, got %d", definitions[i].ID, award.AchievementDefinitionID)
		}

		if award.Status != appdb.AwardStatusPending {
			t.Errorf("expected a pending award for delivery by award reconciliation, got %q", award.Status)
		}
	}

	if !awards[0].AwardedAt.Equal(commitEvent("", 1).OccurredAt) {
		t.Errorf("expected the award to be dated when the activity happened, got %s", awards[0].AwardedAt)
	}
}

func TestProcess_AwardsOnlyOnceAcrossEvents(t *testing.T) {
	conn := testConn(t)
	seedDefinitions(t, conn, catalog.CriteriaCommits, 2)
	eng := New(conn)

	for i := range 5 {
		event := commitEvent("project_event:"+string(rune('a'+i))+":commit", 1)
		if err := eng.Process(t.Context(), event); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	if got := awardCount(t, conn); got != 1 {
		t.Errorf("expected the tier to be awarded once however many events keep clearing it, got %d", got)
	}

	if got := eng.Stats().Awarded; got != 1 {
		t.Errorf("expected 1 award counted, got %d", got)
	}
}

func TestProcess_AwardsNothingBelowThreshold(t *testing.T) {
	conn := testConn(t)
	seedDefinitions(t, conn, catalog.CriteriaCommits, 10)
	eng := New(conn)

	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 9)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got := awardCount(t, conn); got != 0 {
		t.Errorf("expected no award below the threshold, got %d", got)
	}
}

func TestProcess_TreatsUnsetCountAsOne(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	event := commitEvent("project_event:1:commit", 0)

	if err := eng.Process(t.Context(), event); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got := counterFor(t, conn, catalog.CriteriaCommits); got != 1 {
		t.Errorf("expected an unset count to mean one occurrence, got %d", got)
	}
}

func TestProcess_IgnoresUntrackedKindWithoutMarkingItProcessed(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	event := commitEvent("project_event:1:mystery", 1)
	event.Kind = activity.Kind("something_new")

	if err := eng.Process(t.Context(), event); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var processed int64
	if err := conn.Model(&appdb.ProcessedEvent{}).Count(&processed).Error; err != nil {
		t.Fatalf("failed to count processed events: %v", err)
	}

	// Recording it would hide the event from a future engine that does map
	// the kind onto a criteria.
	if processed != 0 {
		t.Errorf("expected an untracked kind to stay unrecorded, got %d processed events", processed)
	}

	if got := eng.Stats().Skipped; got != 1 {
		t.Errorf("expected the event to be counted as skipped, got %d", got)
	}
}

func TestProcess_KeepsUsernameInStepWithGitLab(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 1)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	renamed := commitEvent("project_event:2:commit", 1)
	renamed.ActorUsername = "alice-renamed"

	if err := eng.Process(t.Context(), renamed); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var users []appdb.User
	if err := conn.Find(&users).Error; err != nil {
		t.Fatalf("failed to load users: %v", err)
	}

	if len(users) != 1 {
		t.Fatalf("expected the renamed user to be the same row, got %d users", len(users))
	}

	if users[0].Username != "alice-renamed" {
		t.Errorf("expected username %q, got %q", "alice-renamed", users[0].Username)
	}
}

func TestProcess_CountsSeveralCriteriaForOneKind(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	event := commitEvent("pipeline:1:pipeline_succeeded", 1)
	event.Kind = activity.KindPipelineSucceeded

	if err := eng.Process(t.Context(), event); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got := counterFor(t, conn, catalog.CriteriaPipelinesSucceeded); got != 1 {
		t.Errorf("expected the outcome criteria to be counted, got %d", got)
	}
}
