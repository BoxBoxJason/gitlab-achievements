package backfill

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

const (
	// progressStateKey is the db.SyncState key the resumable cursor of an
	// in-flight backfill is stored under.
	progressStateKey = "backfill:progress"
	// completedStateKey is the db.SyncState key holding the watermark that
	// says the instance's history has been walked end to end. Its presence
	// is what tells the app it can rely on event-driven mode (and, later,
	// the periodic reconciliation sync) rather than a cold start.
	completedStateKey = "backfill:completed_at"
	// cursorDateLayout is the date granularity the events cursor is kept
	// at, matching the granularity GitLab's Events API filters accept.
	cursorDateLayout = "2006-01-02"
)

// phase names the per-project pull a backfill was in the middle of, so an
// interrupted project resumes where it stopped instead of re-fetching the
// pull that already finished.
type phase string

const (
	// phaseEvents is the project's Events API walk (commits, merge
	// requests, issues, notes).
	phaseEvents phase = "events"
	// phasePipelines is the project's pipeline walk, which the Events API
	// doesn't cover.
	phasePipelines phase = "pipelines"
)

// progress is a backfill's resumable position in the instance.
//
// It is stored as a single JSON db.SyncState row rather than one row per
// field so that a flush is one write and can't leave two halves of a
// cursor disagreeing.
//
// Cursor granularity is deliberately coarse: LastProjectID is exact, but
// resuming mid-project re-walks at most one day of events and re-lists (not
// re-fetches) the pipelines already seen. That is sound because the engine
// discards activity it has already processed, so the only cost of a coarse
// cursor is a few repeated reads, not a double-counted commit.
type progress struct {
	// EventsCursor is the date of the last event processed for
	// CurrentProjectID, in cursorDateLayout format.
	EventsCursor string `json:"events_cursor,omitempty"`
	// Phase is which per-project pull was in flight.
	Phase phase `json:"phase,omitempty"`
	// LastProjectID is the highest project ID walked to completion.
	// Projects are visited in ascending ID order, so every project at or
	// below it is done.
	LastProjectID int64 `json:"last_project_id,omitempty"`
	// CurrentProjectID is the project that was in flight, or 0 when the
	// walk stopped cleanly between projects.
	CurrentProjectID int64 `json:"current_project_id,omitempty"`
	// LastPipelineID is the highest pipeline ID processed for
	// CurrentProjectID.
	LastPipelineID int64 `json:"last_pipeline_id,omitempty"`
}

// startProject resets the per-project cursors unless projectID is the
// project the previous run was interrupted in, in which case its saved
// cursors are kept so the walk picks up where it left off.
func (p *progress) startProject(projectID int64) {
	if p.CurrentProjectID == projectID {
		return
	}

	p.CurrentProjectID = projectID
	p.Phase = phaseEvents
	p.EventsCursor = ""
	p.LastPipelineID = 0
}

// finishProject marks projectID fully walked and clears the per-project
// cursors.
func (p *progress) finishProject(projectID int64) {
	p.LastProjectID = projectID
	p.CurrentProjectID = 0
	p.Phase = ""
	p.EventsCursor = ""
	p.LastPipelineID = 0
}

// eventsFloor returns the earliest date the current project's event walk
// still needs to cover, given the configured look-back window and the saved
// cursor. A zero time means "no lower bound": walk the project's full
// history.
func (p *progress) eventsFloor(since time.Time) time.Time {
	cursor, err := time.Parse(cursorDateLayout, p.EventsCursor)
	if err != nil {
		return since
	}

	if since.IsZero() || cursor.After(since) {
		return cursor
	}

	return since
}

// CompletedAt reports when the instance's history was last walked end to
// end, and whether it ever was. Callers that only run once the cold start
// is over (event-driven mode, the periodic reconciliation sync) gate on it.
func CompletedAt(conn *gorm.DB) (time.Time, bool, error) {
	value, found, err := loadState(conn, completedStateKey)
	if err != nil || !found {
		return time.Time{}, false, err
	}

	completedAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("stored backfill completion watermark %q is not an RFC 3339 timestamp: %w", value, err)
	}

	return completedAt, true, nil
}

// markCompleted records completedAt as the moment the walk finished and
// drops the now-meaningless in-flight cursor.
func markCompleted(conn *gorm.DB, completedAt time.Time) error {
	err := saveState(conn, completedStateKey, completedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}

	return clearProgress(conn)
}

// loadProgress reads the saved cursor, returning a zero progress when no
// backfill has started yet.
func loadProgress(conn *gorm.DB) (progress, error) {
	var saved progress

	value, found, err := loadState(conn, progressStateKey)
	if err != nil || !found {
		return saved, err
	}

	err = json.Unmarshal([]byte(value), &saved)
	if err != nil {
		return progress{}, fmt.Errorf("stored backfill progress %q is not valid JSON: %w", value, err)
	}

	return saved, nil
}

// saveProgress persists the cursor a resumed run picks up from.
func saveProgress(conn *gorm.DB, saved progress) error {
	encoded, err := json.Marshal(saved)
	if err != nil {
		return fmt.Errorf("failed to encode backfill progress: %w", err)
	}

	return saveState(conn, progressStateKey, string(encoded))
}

// clearProgress removes the in-flight cursor.
func clearProgress(conn *gorm.DB) error {
	err := conn.Where("key = ?", progressStateKey).Delete(&db.SyncState{}).Error
	if err != nil {
		return fmt.Errorf("failed to clear backfill progress: %w", err)
	}

	return nil
}

// loadState reads a db.SyncState value by key, reporting whether it exists.
func loadState(conn *gorm.DB, key string) (string, bool, error) {
	var state db.SyncState

	err := conn.Where("key = ?", key).First(&state).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("failed to load sync state %q: %w", key, err)
	}

	return state.Value, true, nil
}

// saveState writes a db.SyncState value, creating the row on first use and
// overwriting it thereafter.
func saveState(conn *gorm.DB, key, value string) error {
	var state db.SyncState

	err := conn.Where("key = ?", key).First(&state).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		err = conn.Create(&db.SyncState{Key: key, Value: value}).Error
	case err != nil:
		return fmt.Errorf("failed to load sync state %q: %w", key, err)
	default:
		state.Value = value
		err = conn.Save(&state).Error
	}

	if err != nil {
		return fmt.Errorf("failed to persist sync state %q: %w", key, err)
	}

	return nil
}
