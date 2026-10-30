package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
)

// projectEvent is a commit by alice in projectID, keyed on n so that each
// call is a distinct event.
func projectEvent(n int, projectID int64) activity.Event {
	return activity.Event{
		OccurredAt:    time.Date(2024, time.May, 3, 10, n, 0, 0, time.UTC),
		Kind:          activity.KindCommit,
		DedupKey:      fmt.Sprintf("push:%d:%d:commit", projectID, n),
		ActorUsername: "alice",
		ActorID:       100,
		ProjectID:     projectID,
		Count:         1,
	}
}

func process(t *testing.T, eng *Engine, events ...activity.Event) {
	t.Helper()

	for _, event := range events {
		if err := eng.Process(t.Context(), event); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}
}

func TestProcess_CountsDistinctProjects(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	// Three events, two projects, and one with no project at all.
	process(t, eng, projectEvent(1, 7), projectEvent(2, 7), projectEvent(3, 8), projectEvent(4, 0))

	if got := counterOrZero(t, conn, catalog.CriteriaProjectsContributed); got != 2 {
		t.Errorf("expected 2 projects, got %d", got)
	}
}

func TestProcess_AwardsProjectTiers(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaProjectsContributed, tier{threshold: 1, exp: 1}, tier{threshold: 2, exp: 3})

	eng := New(conn)
	process(t, eng, projectEvent(1, 7), projectEvent(2, 8))

	if got := eng.Stats().Awarded; got != 2 {
		t.Errorf("expected both project tiers awarded, got %d", got)
	}
}

// A repeat counts nothing again, but the project it happened in is still
// news to a set that has never heard of it. That is what lets a forced
// backfill fill the set in on an instance walked before it existed.
func TestProcess_ARepeatFillsInAProjectTheSetLacks(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	first := projectEvent(1, 7)
	process(t, eng, first)

	// The state an instance upgraded from before the set existed is in:
	// the event is in the processed log, the project is not in the set.
	if err := conn.Where("1 = 1").Delete(&appdb.ActivityProject{}).Error; err != nil {
		t.Fatalf("failed to clear the project set: %v", err)
	}

	if err := conn.Model(&appdb.ProgressCounter{}).Where("criteria_key = ?", catalog.CriteriaProjectsContributed).Update("count", 0).Error; err != nil {
		t.Fatalf("failed to reset the counter: %v", err)
	}

	process(t, eng, first)

	if got := counterOrZero(t, conn, catalog.CriteriaProjectsContributed); got != 1 {
		t.Errorf("expected the repeat to restore the project, got %d", got)
	}

	if got := counterOrZero(t, conn, catalog.CriteriaCommits); got != 1 {
		t.Errorf("expected the repeat not to count the commit again, got %d", got)
	}
}

func TestProcess_ARepeatAddsNothingForAnExcludedUser(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	first := projectEvent(1, 7)
	process(t, eng, first)

	err := conn.Where("1 = 1").Delete(&appdb.ActivityProject{}).Error
	if err != nil {
		t.Fatalf("failed to clear the project set: %v", err)
	}

	err = conn.Model(&appdb.User{}).Where(&appdb.User{GitLabUserID: first.ActorID}).
		Update("excluded_reason", string(exclusion.OptedOut)).Error
	if err != nil {
		t.Fatalf("failed to exclude the user: %v", err)
	}

	process(t, eng, first)

	var count int64
	if err := conn.Model(&appdb.ActivityProject{}).Count(&count).Error; err != nil {
		t.Fatalf("failed to count projects: %v", err)
	}

	if count != 0 {
		t.Errorf("expected nothing recorded for an excluded user, got %d projects", count)
	}
}
