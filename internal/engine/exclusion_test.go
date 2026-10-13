package engine

import (
	"context"
	"errors"
	"testing"

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
