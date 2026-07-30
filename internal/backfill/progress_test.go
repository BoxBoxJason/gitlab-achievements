package backfill

import (
	"testing"
	"time"

	"gorm.io/gorm"

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

func TestProgress_RoundTrips(t *testing.T) {
	conn := testConn(t)

	saved := progress{
		EventsCursor:     "2024-05-03",
		Phase:            phasePipelines,
		LastProjectID:    12,
		CurrentProjectID: 13,
		LastPipelineID:   900,
	}

	if err := saveProgress(conn, saved); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	loaded, err := loadProgress(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if loaded != saved {
		t.Errorf("expected %+v, got %+v", saved, loaded)
	}
}

func TestProgress_OverwritesRatherThanAccumulating(t *testing.T) {
	conn := testConn(t)

	for _, projectID := range []int64{1, 2, 3} {
		if err := saveProgress(conn, progress{LastProjectID: projectID}); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	var rows int64
	if err := conn.Model(&appdb.SyncState{}).Where("key = ?", progressStateKey).Count(&rows).Error; err != nil {
		t.Fatalf("failed to count sync state rows: %v", err)
	}

	if rows != 1 {
		t.Errorf("expected the cursor to stay a single row, got %d", rows)
	}

	loaded, err := loadProgress(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if loaded.LastProjectID != 3 {
		t.Errorf("expected the latest cursor, got %+v", loaded)
	}
}

func TestLoadProgress_EmptyBeforeAnyRun(t *testing.T) {
	conn := testConn(t)

	loaded, err := loadProgress(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if loaded != (progress{}) {
		t.Errorf("expected a zero cursor, got %+v", loaded)
	}
}

func TestLoadProgress_RejectsCorruptState(t *testing.T) {
	conn := testConn(t)

	if err := conn.Create(&appdb.SyncState{Key: progressStateKey, Value: "not json"}).Error; err != nil {
		t.Fatalf("failed to seed corrupt state: %v", err)
	}

	_, err := loadProgress(conn)
	if err == nil {
		t.Fatal("expected an error for unreadable progress, got nil")
	}
}

func TestCompletedAt_ReportsTheWatermark(t *testing.T) {
	conn := testConn(t)

	_, found, err := CompletedAt(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if found {
		t.Fatal("expected no watermark before any backfill ran")
	}

	finishedAt := time.Date(2024, time.May, 3, 10, 0, 0, 0, time.UTC)

	if err := markCompleted(conn, finishedAt); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	completedAt, found, err := CompletedAt(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !found || !completedAt.Equal(finishedAt) {
		t.Errorf("expected %s, got %s (found=%t)", finishedAt, completedAt, found)
	}
}

func TestMarkCompleted_DropsTheInFlightCursor(t *testing.T) {
	conn := testConn(t)

	if err := saveProgress(conn, progress{LastProjectID: 12, CurrentProjectID: 13}); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if err := markCompleted(conn, time.Now()); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	loaded, err := loadProgress(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if loaded != (progress{}) {
		t.Errorf("expected the cursor to be cleared once history was fully walked, got %+v", loaded)
	}
}

func TestProgress_StartProjectKeepsCursorsOfTheInterruptedProject(t *testing.T) {
	interrupted := progress{
		EventsCursor:     "2024-05-03",
		Phase:            phasePipelines,
		LastProjectID:    12,
		CurrentProjectID: 13,
		LastPipelineID:   900,
	}

	resumed := interrupted
	resumed.startProject(13)

	if resumed != interrupted {
		t.Errorf("expected the interrupted project's cursors to be kept, got %+v", resumed)
	}

	next := interrupted
	next.startProject(14)

	want := progress{Phase: phaseEvents, LastProjectID: 12, CurrentProjectID: 14}
	if next != want {
		t.Errorf("expected a fresh project to reset the per-project cursors, got %+v", next)
	}
}

func TestProgress_FinishProjectClearsPerProjectCursors(t *testing.T) {
	done := progress{
		EventsCursor:     "2024-05-03",
		Phase:            phasePipelines,
		LastProjectID:    12,
		CurrentProjectID: 13,
		LastPipelineID:   900,
	}

	done.finishProject(13)

	if done != (progress{LastProjectID: 13}) {
		t.Errorf("expected only the completed project ID to survive, got %+v", done)
	}
}

func TestProgress_EventsFloor(t *testing.T) {
	since := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		state progress
		since time.Time
		want  time.Time
	}{
		{
			name:  "no window and no cursor walks everything",
			state: progress{},
			want:  time.Time{},
		},
		{
			name:  "window alone",
			state: progress{},
			since: since,
			want:  since,
		},
		{
			name:  "cursor alone",
			state: progress{EventsCursor: "2024-05-03"},
			want:  time.Date(2024, time.May, 3, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "a cursor past the window wins, so resuming never re-walks",
			state: progress{EventsCursor: "2024-05-03"},
			since: since,
			want:  time.Date(2024, time.May, 3, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "a window past the cursor wins, so a narrowed window is honored",
			state: progress{EventsCursor: "2023-01-01"},
			since: since,
			want:  since,
		},
		{
			name:  "an unreadable cursor falls back to the window",
			state: progress{EventsCursor: "garbage"},
			since: since,
			want:  since,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.state.eventsFloor(tc.since)
			if !got.Equal(tc.want) {
				t.Errorf("expected %s, got %s", tc.want, got)
			}
		})
	}
}
