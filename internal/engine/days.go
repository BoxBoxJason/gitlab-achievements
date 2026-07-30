package engine

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

const (
	// dayLayout is the calendar-day form db.ActivityDay.Date is kept in.
	dayLayout = "2006-01-02"
	// nightOwlUntilHour ends the small hours: activity from midnight up to
	// this hour makes the day a night-owl day.
	nightOwlUntilHour = 5
	// earlyBirdUntilHour ends the early morning, which starts where the
	// small hours end. The two windows don't overlap, so no single moment
	// is both.
	earlyBirdUntilHour = 8
)

// dayCriteria are the criteria derived from which days a user was active
// rather than from a running total of events.
//
// They are recomputed from the db.ActivityDay set rather than incremented,
// because none of them is a sum of events: two commits on one afternoon are
// one active day, and a streak can be extended by a day landing between two
// existing ones, which no forward-only counter could notice. Recomputing
// also makes them immune to the order activity arrives in, which the
// project-by-project backfill offers no guarantees about.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var dayCriteria = []string{
	catalog.CriteriaActiveDays,
	catalog.CriteriaActivityStreak,
	catalog.CriteriaNightOwlDays,
	catalog.CriteriaEarlyBirdDays,
}

// dayTotals is what one user's activity-day set adds up to.
type dayTotals struct {
	active    int64
	streak    int64
	nightOwl  int64
	earlyBird int64
}

// value returns the total for criteriaKey.
func (t dayTotals) value(criteriaKey string) int64 {
	switch criteriaKey {
	case catalog.CriteriaActiveDays:
		return t.active
	case catalog.CriteriaActivityStreak:
		return t.streak
	case catalog.CriteriaNightOwlDays:
		return t.nightOwl
	case catalog.CriteriaEarlyBirdDays:
		return t.earlyBird
	}

	return 0
}

// recordActivityDay marks the calendar day occurredAt falls on as one the
// user was active on, latching on the night-owl and early-bird flags if the
// time of day calls for it.
//
// It reports whether anything actually changed, so the caller only pays for
// recomputing the derived criteria on the events that could move them:
// after the first event of a day, every other event that day is a no-op.
//
// Activity with no usable timestamp is ignored rather than attributed to
// today: dating years-old history to the day the backfill ran would invent
// streaks that never happened.
func recordActivityDay(txn *gorm.DB, userID int64, occurredAt time.Time) (bool, error) {
	if occurredAt.IsZero() {
		return false, nil
	}

	date := occurredAt.Format(dayLayout)
	nightOwl, earlyBird := clockWindows(occurredAt)

	var day db.ActivityDay

	err := txn.Where(&db.ActivityDay{UserID: userID, Date: date}).First(&day).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		day = db.ActivityDay{UserID: userID, Date: date, NightOwl: nightOwl, EarlyBird: earlyBird}

		err = txn.Create(&day).Error
		if err != nil {
			return false, fmt.Errorf("failed to record activity on %s for user %d: %w", date, userID, err)
		}

		return true, nil
	case err != nil:
		return false, fmt.Errorf("failed to load activity on %s for user %d: %w", date, userID, err)
	}

	return latchClockWindows(txn, &day, nightOwl, earlyBird)
}

// clockWindows reports which of the two notable parts of the clock a moment
// falls in. They are exclusive of each other, so no single event both
// counts as burning the midnight oil and as being up with the lark.
func clockWindows(occurredAt time.Time) (nightOwl, earlyBird bool) {
	hour := occurredAt.Hour()

	return hour < nightOwlUntilHour, hour >= nightOwlUntilHour && hour < earlyBirdUntilHour
}

// latchClockWindows turns on whichever flags a known day is missing,
// reporting whether it had to change anything.
func latchClockWindows(txn *gorm.DB, day *db.ActivityDay, nightOwl, earlyBird bool) (bool, error) {
	if (!nightOwl || day.NightOwl) && (!earlyBird || day.EarlyBird) {
		return false, nil
	}

	day.NightOwl = day.NightOwl || nightOwl
	day.EarlyBird = day.EarlyBird || earlyBird

	err := txn.Model(day).Updates(map[string]any{"night_owl": day.NightOwl, "early_bird": day.EarlyBird}).Error
	if err != nil {
		return false, fmt.Errorf("failed to update activity on %s for user %d: %w", day.Date, day.UserID, err)
	}

	return true, nil
}

// computeDayTotals reduces a user's whole activity-day set to the values
// its criteria are earned off.
//
// The streak it reports is the longest run the user ever managed, not the
// run they happen to be on. A GitLab achievement, once awarded, is not
// taken back, so a criteria that can fall as well as rise would award a
// tier the first time it was reached and mean nothing ever after. The
// longest run is also the only answer a backfill of years-old history can
// honestly give: "currently" is not a property of 2019.
func computeDayTotals(txn *gorm.DB, userID int64) (dayTotals, error) {
	var days []db.ActivityDay

	err := txn.Where(&db.ActivityDay{UserID: userID}).Order("date").Find(&days).Error
	if err != nil {
		return dayTotals{}, fmt.Errorf("failed to load activity days for user %d: %w", userID, err)
	}

	totals := dayTotals{active: int64(len(days))}

	var (
		run      int64
		previous time.Time
	)

	for _, day := range days {
		if day.NightOwl {
			totals.nightOwl++
		}

		if day.EarlyBird {
			totals.earlyBird++
		}

		date, parseErr := time.Parse(dayLayout, day.Date)
		if parseErr != nil {
			continue
		}

		if !previous.IsZero() && date.Equal(previous.AddDate(0, 0, 1)) {
			run++
		} else {
			run = 1
		}

		previous = date

		if run > totals.streak {
			totals.streak = run
		}
	}

	return totals, nil
}
