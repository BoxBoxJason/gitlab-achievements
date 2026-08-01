package reconcile

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

const (
	// completedStateKey is the db.SyncState key holding the moment the last
	// successful reconciliation pass began. It is the app's record of how
	// far back the read side is known to be correct.
	completedStateKey = "reconcile:completed_at"
	// watermarkSlack is how far before the last successful pass the next
	// one still reaches.
	//
	// The watermark is the moment a pass started, not the moment it
	// finished, so activity that happened while it ran is already covered
	// by taking it at face value. This is the margin on top of that, for
	// the clock skew between this app and the instance it reads: an
	// instance whose clock runs behind would otherwise date activity just
	// under a watermark that was never meant to exclude it.
	watermarkSlack = time.Hour
)

// CompletedAt reports when the last successful reconciliation pass began,
// and whether there has ever been one.
//
// It is the app's freshness signal for the read side: the further behind it
// falls, the longer a lost webhook delivery has gone unhealed.
func CompletedAt(conn *gorm.DB) (time.Time, bool, error) {
	var state db.SyncState

	err := conn.Where("key = ?", completedStateKey).First(&state).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("failed to load the reconciliation watermark: %w", err)
	}

	completedAt, err := time.Parse(time.RFC3339, state.Value)
	if err != nil {
		return time.Time{}, false, fmt.Errorf(
			"stored reconciliation watermark %q is not an RFC 3339 timestamp: %w", state.Value, err)
	}

	return completedAt, true, nil
}

// window resolves the span a pass covers, and how far behind the watermark
// had fallen.
//
// The span is the configured look-back at minimum. It widens to cover
// everything since the last successful pass whenever that is further back,
// which is what makes a missed pass (or a week of downtime) heal itself
// rather than leave a hole nothing will ever revisit. It is deliberately
// not capped: a window narrower than the gap would silently abandon the
// part it didn't reach, and the cost of a wide one is requests, which the
// caller's rate limit already bounds.
//
// The returned gap is how much of the window came from the watermark
// rather than from the look-back, so a caller can report a pass that had
// ground to make up as distinct from a routine one.
func window(conn *gorm.DB, startedAt time.Time, lookback time.Duration) (time.Time, time.Duration, error) {
	since := startedAt.Add(-lookback)

	completedAt, found, err := CompletedAt(conn)
	if err != nil {
		return time.Time{}, 0, err
	}

	if !found {
		return since, 0, nil
	}

	fromWatermark := completedAt.Add(-watermarkSlack)
	if !fromWatermark.Before(since) {
		return since, 0, nil
	}

	return fromWatermark, since.Sub(fromWatermark), nil
}

// markCompleted records startedAt as the point the read side is correct up
// to.
//
// It stores when the pass began rather than when it ended on purpose:
// activity that happened while the pass was running may have been walked
// before it existed, so a watermark at the finish line would step over it.
func markCompleted(conn *gorm.DB, startedAt time.Time) error {
	value := startedAt.UTC().Format(time.RFC3339)

	var state db.SyncState

	err := conn.Where("key = ?", completedStateKey).First(&state).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		err = conn.Create(&db.SyncState{Key: completedStateKey, Value: value}).Error
	case err != nil:
		return fmt.Errorf("failed to load the reconciliation watermark: %w", err)
	default:
		state.Value = value
		err = conn.Save(&state).Error
	}

	if err != nil {
		return fmt.Errorf("failed to persist the reconciliation watermark: %w", err)
	}

	return nil
}
