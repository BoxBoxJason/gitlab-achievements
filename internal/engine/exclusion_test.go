package engine

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
)

type stubFilter struct {
	err     error
	reasons map[int64]exclusion.Reason
}

func (f stubFilter) Check(_ context.Context, gitlabUserID int64, _ string) (exclusion.Reason, error) {
	return f.reasons[gitlabUserID], f.err
}

func TestProcess_SkipsActivityFromExcludedUsers(t *testing.T) {
	conn := testConn(t)
	eng := New(conn, WithFilter(stubFilter{reasons: map[int64]exclusion.Reason{100: exclusion.Bot}}))

	err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 3))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var processed int64
	if err := conn.Model(&appdb.ProcessedEvent{}).Count(&processed).Error; err != nil {
		t.Fatalf("failed to count processed events: %v", err)
	}

	if processed != 0 {
		t.Error("expected an excluded user's activity not to be marked processed")
	}

	var counters int64
	if err := conn.Model(&appdb.ProgressCounter{}).Count(&counters).Error; err != nil {
		t.Fatalf("failed to count counters: %v", err)
	}

	if counters != 0 {
		t.Errorf("expected nothing counted for an excluded user, got %d counters", counters)
	}

	if stats := eng.Stats(); stats.Skipped != 1 || stats.Processed != 0 {
		t.Errorf("expected the activity to be skipped, got %+v", stats)
	}
}

func TestProcess_ReportsAFilterFailure(t *testing.T) {
	conn := testConn(t)
	eng := New(conn, WithFilter(stubFilter{err: errors.New("database unavailable")}))

	err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 3))
	if err == nil {
		t.Fatal("expected a failed exclusion check to fail the evaluation, so the queue retries it")
	}
}

// optingOutFilter lets the activity through and then opts its actor out
// before the engine's transaction begins, the window the filter's verdict
// leaves open since it is reached outside the transaction.
type optingOutFilter struct {
	conn *gorm.DB
}

func (f optingOutFilter) Check(_ context.Context, gitlabUserID int64, username string) (exclusion.Reason, error) {
	user := appdb.User{GitLabUserID: gitlabUserID, Username: username}
	if err := f.conn.Where(&appdb.User{GitLabUserID: gitlabUserID}).FirstOrCreate(&user).Error; err != nil {
		return exclusion.Included, err
	}

	err := f.conn.Model(&user).Update("excluded_reason", string(exclusion.OptedOut)).Error

	return exclusion.Included, err
}

func TestProcess_CountsNothingForAnOptOutRacingTheFilter(t *testing.T) {
	conn := testConn(t)
	eng := New(conn, WithFilter(optingOutFilter{conn: conn}))

	err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 3))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var counters, processed int64
	if err := conn.Model(&appdb.ProgressCounter{}).Count(&counters).Error; err != nil {
		t.Fatalf("failed to count counters: %v", err)
	}

	if err := conn.Model(&appdb.ProcessedEvent{}).Count(&processed).Error; err != nil {
		t.Fatalf("failed to count processed events: %v", err)
	}

	if counters != 0 || processed != 0 {
		t.Errorf("expected nothing kept for a user excluded mid-evaluation, got %d counters and %d processed", counters, processed)
	}

	if stats := eng.Stats(); stats.Skipped != 1 || stats.Processed != 0 {
		t.Errorf("expected the activity to be skipped, got %+v", stats)
	}
}

// TestProcess_IgnoresActivityWithoutAGitLabActor covers a forged payload
// carrying a negative actor ID: no GitLab user has one, so nothing it does
// may create a participant.
func TestProcess_IgnoresActivityWithoutAGitLabActor(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	event := commitEvent("project_event:1:commit", 3)
	event.ActorID = -1

	if err := eng.Process(t.Context(), event); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var users int64
	if err := conn.Model(&appdb.User{}).Count(&users).Error; err != nil {
		t.Fatalf("failed to count users: %v", err)
	}

	if users != 0 {
		t.Errorf("expected no user created for a negative actor ID, got %d", users)
	}
}
