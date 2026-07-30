package engine

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// dayEvent is an activity by the same user at a given moment, with a dedup
// key derived from it so distinct moments are distinct events.
func dayEvent(at time.Time) activity.Event {
	return activity.Event{
		OccurredAt:    at,
		Kind:          activity.KindCommit,
		DedupKey:      "project_event:" + at.Format(time.RFC3339) + ":commit",
		ActorUsername: "alice",
		ActorID:       100,
		Count:         1,
	}
}

func at(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
}

// processAll feeds moments to the engine in the order given.
func processAll(t *testing.T, eng *Engine, moments ...time.Time) {
	t.Helper()

	for _, moment := range moments {
		if err := eng.Process(t.Context(), dayEvent(moment)); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}
}

func counterOrZero(t *testing.T, conn *gorm.DB, criteriaKey string) int64 {
	t.Helper()

	var counter appdb.ProgressCounter

	err := conn.Where("criteria_key = ?", criteriaKey).First(&counter).Error
	if err != nil {
		return 0
	}

	return counter.Count
}

func TestProcess_CountsDistinctActiveDays(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	// Three events, two days: an active day is a day, not an event count.
	processAll(t, eng,
		at(2024, time.May, 3, 10),
		at(2024, time.May, 3, 14),
		at(2024, time.May, 7, 9),
	)

	if got := counterOrZero(t, conn, catalog.CriteriaActiveDays); got != 2 {
		t.Errorf("expected 2 active days, got %d", got)
	}
}

func TestProcess_TracksTheLongestStreak(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	// Four in a row, a gap, then two: the longest run is what counts, and
	// the later short run must not overwrite it.
	processAll(t, eng,
		at(2024, time.May, 1, 10),
		at(2024, time.May, 2, 10),
		at(2024, time.May, 3, 10),
		at(2024, time.May, 4, 10),
		at(2024, time.May, 9, 10),
		at(2024, time.May, 10, 10),
	)

	if got := counterOrZero(t, conn, catalog.CriteriaActivityStreak); got != 4 {
		t.Errorf("expected a longest streak of 4, got %d", got)
	}
}

func TestProcess_StreakIsIndependentOfArrivalOrder(t *testing.T) {
	// The backfill walks project by project, so a user's days arrive in
	// whatever order their projects happen to be in. A day landing between
	// two already-known ones has to join them into one run.
	forwards := testConn(t)
	processAll(t, New(forwards),
		at(2024, time.May, 1, 10),
		at(2024, time.May, 2, 10),
		at(2024, time.May, 3, 10),
	)

	backwards := testConn(t)
	processAll(t, New(backwards),
		at(2024, time.May, 3, 10),
		at(2024, time.May, 1, 10),
		at(2024, time.May, 2, 10),
	)

	want := counterOrZero(t, forwards, catalog.CriteriaActivityStreak)
	got := counterOrZero(t, backwards, catalog.CriteriaActivityStreak)

	if want != 3 {
		t.Fatalf("expected a streak of 3 in date order, got %d", want)
	}

	if got != want {
		t.Errorf("expected the same streak whatever order the days arrived in, got %d and %d", want, got)
	}
}

func TestProcess_StreakSpansMonthAndYearBoundaries(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	processAll(t, eng,
		at(2023, time.December, 30, 10),
		at(2023, time.December, 31, 10),
		at(2024, time.January, 1, 10),
		at(2024, time.January, 2, 10),
	)

	if got := counterOrZero(t, conn, catalog.CriteriaActivityStreak); got != 4 {
		t.Errorf("expected the streak to cross the year boundary, got %d", got)
	}
}

func TestProcess_TracksNightOwlAndEarlyBirdDays(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	processAll(t, eng,
		at(2024, time.May, 1, 2),  // night owl
		at(2024, time.May, 2, 6),  // early bird
		at(2024, time.May, 3, 14), // neither
		at(2024, time.May, 4, 4),  // night owl
	)

	if got := counterOrZero(t, conn, catalog.CriteriaNightOwlDays); got != 2 {
		t.Errorf("expected 2 night owl days, got %d", got)
	}

	if got := counterOrZero(t, conn, catalog.CriteriaEarlyBirdDays); got != 1 {
		t.Errorf("expected 1 early bird day, got %d", got)
	}
}

func TestProcess_NightOwlDayCountsOnceHoweverBusy(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	processAll(t, eng,
		at(2024, time.May, 1, 1),
		at(2024, time.May, 1, 2),
		at(2024, time.May, 1, 3),
	)

	if got := counterOrZero(t, conn, catalog.CriteriaNightOwlDays); got != 1 {
		t.Errorf("expected one night owl day for one night, got %d", got)
	}
}

func TestProcess_LatchesTheFlagOnALaterEventInTheSameDay(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	// The afternoon comes first, so the day already exists by the time the
	// small-hours event for it arrives.
	processAll(t, eng,
		at(2024, time.May, 1, 15),
		at(2024, time.May, 1, 3),
	)

	if got := counterOrZero(t, conn, catalog.CriteriaNightOwlDays); got != 1 {
		t.Errorf("expected the existing day to be marked a night owl day, got %d", got)
	}
}

func TestProcess_AwardsDayDerivedTiers(t *testing.T) {
	conn := testConn(t)
	seedDefinitions(t, conn, catalog.CriteriaActivityStreak, 3)
	eng := New(conn)

	processAll(t, eng,
		at(2024, time.May, 1, 10),
		at(2024, time.May, 2, 10),
	)

	if got := awardCount(t, conn); got != 0 {
		t.Fatalf("expected no award two days into a three-day streak, got %d", got)
	}

	processAll(t, eng, at(2024, time.May, 3, 10))

	if got := awardCount(t, conn); got != 1 {
		t.Errorf("expected the streak tier to be awarded on the third day, got %d", got)
	}
}

func TestProcess_IgnoresUndatedActivityForDayCriteria(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	// Dating an activity GitLab couldn't timestamp to today would invent a
	// day the user was never active on.
	event := dayEvent(time.Time{})
	event.DedupKey = "project_event:1:commit"

	if err := eng.Process(t.Context(), event); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got := counterOrZero(t, conn, catalog.CriteriaActiveDays); got != 0 {
		t.Errorf("expected undated activity not to count as an active day, got %d", got)
	}

	if got := counterFor(t, conn, catalog.CriteriaCommits); got != 1 {
		t.Errorf("expected the commit itself to still count, got %d", got)
	}
}

func TestProcess_ReplayedActivityDoesNotInflateDayCriteria(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	moment := at(2024, time.May, 1, 10)
	processAll(t, eng, moment, moment, moment)

	if got := counterOrZero(t, conn, catalog.CriteriaActiveDays); got != 1 {
		t.Errorf("expected one active day, got %d", got)
	}

	var days int64
	if err := conn.Model(&appdb.ActivityDay{}).Count(&days).Error; err != nil {
		t.Fatalf("failed to count activity days: %v", err)
	}

	if days != 1 {
		t.Errorf("expected a single activity day row, got %d", days)
	}
}

func TestProcess_DayCriteriaAreScopedPerUser(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	processAll(t, eng, at(2024, time.May, 1, 10), at(2024, time.May, 2, 10))

	other := dayEvent(at(2024, time.May, 1, 10))
	other.ActorID = 200
	other.ActorUsername = "bob"
	other.DedupKey = "project_event:bob:commit"

	if err := eng.Process(t.Context(), other); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var counters []appdb.ProgressCounter
	if err := conn.Where("criteria_key = ?", catalog.CriteriaActiveDays).Order("user_id").Find(&counters).Error; err != nil {
		t.Fatalf("failed to load counters: %v", err)
	}

	if len(counters) != 2 {
		t.Fatalf("expected one counter per user, got %d", len(counters))
	}

	if counters[0].Count != 2 || counters[1].Count != 1 {
		t.Errorf("expected each user's own day count, got %d and %d", counters[0].Count, counters[1].Count)
	}
}

func TestRecordActivityDay_UsesTheReportedClock(t *testing.T) {
	conn := testConn(t)
	eng := New(conn)

	// 01:30 at +02:00 is 23:30 the previous day in UTC. The day and hour
	// the instance reported are what the criteria are about, so this must
	// land on the 2nd as a night-owl day, not on the 1st as an evening.
	zone := time.FixedZone("test", 2*60*60)
	processAll(t, eng, time.Date(2024, time.May, 2, 1, 30, 0, 0, zone))

	var day appdb.ActivityDay
	if err := conn.First(&day).Error; err != nil {
		t.Fatalf("failed to load activity day: %v", err)
	}

	if day.Date != "2024-05-02" {
		t.Errorf("expected the reported date, got %q", day.Date)
	}

	if !day.NightOwl {
		t.Error("expected the reported hour to make it a night owl day")
	}
}
