// Package engine turns normalized activity into achievement awards.
//
// It is the single evaluation path both activity producers feed: the
// historical backfill and (once implemented) live webhook ingestion both
// hand it activity.Event values, so "does this activity award or advance an
// achievement" is answered in exactly one place rather than once per source.
//
// Criteria come in two shapes and both are evaluated here. Cumulative ones
// (commits, merge requests, pipelines) are running totals every matching
// event advances. Day-derived ones (active days, streaks, night-owl and
// early-bird days) are recomputed from the set of days the user was active
// on, because they aren't sums of events: two commits in one afternoon are
// one active day, and a streak can be extended by a day arriving between
// two already-known ones. Either way, every catalog tier whose threshold
// the new value reaches is awarded, and the tier's EXP reward is added to
// the user's total in the same transaction.
//
// Awards are recorded locally as db.AwardStatusPending rather than pushed to
// GitLab inline. bootstrap.ReconcileAwards already owns delivery and retry
// for unconfirmed awards, so keeping the engine free of the GitLab write
// client both avoids a second delivery path and keeps evaluation testable
// with nothing but a database.
//
// EXP has no GitLab counterpart at all, so this package is the only thing
// that maintains it. See RecomputeEXP for why it is derived from the awards
// a user holds rather than accumulated forward.
package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// Engine evaluates activity against the achievement definitions stored in
// the database by bootstrap. It is safe for concurrent use.
type Engine struct {
	conn      *gorm.DB
	processed atomic.Int64
	skipped   atomic.Int64
	awarded   atomic.Int64
}

// New builds an Engine writing to conn.
func New(conn *gorm.DB) *Engine {
	return &Engine{conn: conn}
}

// Stats summarizes what an Engine has done since it was built.
type Stats struct {
	// Processed counts events that advanced at least one counter.
	Processed int64
	// Skipped counts events discarded as already processed or as carrying
	// no criteria this engine tracks.
	Skipped int64
	// Awarded counts achievement tiers newly earned.
	Awarded int64
}

// Stats returns a snapshot of the engine's counters.
func (e *Engine) Stats() Stats {
	return Stats{
		Processed: e.processed.Load(),
		Skipped:   e.skipped.Load(),
		Awarded:   e.awarded.Load(),
	}
}

// Process evaluates one activity event: it records the event as processed,
// increments the criteria counters it maps to, and records an award for
// every catalog tier the new counts reach.
//
// The whole evaluation runs in one transaction, so an event is either fully
// counted or not counted at all: a crash mid-event can't leave a counter
// incremented without its awards, or an event marked processed without its
// counter increment. Replaying an already-processed event (a resumed
// backfill re-walking a page, a redelivered hook) is a no-op.
func (e *Engine) Process(ctx context.Context, event activity.Event) error {
	criteria := criteriaFor(event.Kind)
	if len(criteria) == 0 {
		// Deliberately not recorded as processed: a future engine that
		// maps this kind onto a criteria should still see the event on a
		// re-run rather than find it marked done and skip it forever.
		e.skipped.Add(1)

		return nil
	}

	var (
		counted bool
		awarded int
	)

	err := e.conn.WithContext(ctx).Transaction(func(txn *gorm.DB) error {
		fresh, err := recordProcessed(txn, event)
		if err != nil || !fresh {
			return err
		}

		counted = true

		awarded, err = evaluate(txn, event, criteria)

		return err
	})
	if err != nil {
		return fmt.Errorf("failed to evaluate %s activity %q: %w", event.Kind, event.DedupKey, err)
	}

	if counted {
		e.processed.Add(1)
	} else {
		e.skipped.Add(1)
	}

	e.awarded.Add(int64(awarded))

	return nil
}

// evaluate applies event to every criteria it advances, inside txn: the
// cumulative ones its kind maps to, and the day-derived ones every dated
// activity feeds regardless of what it was. It returns how many tiers were
// newly awarded.
//
// The user's EXP total is settled here, at the end, rather than by each
// award as it lands: one recomputation covers however many tiers an event
// crossed, and it runs in the same transaction as the awards themselves, so
// a crash can't commit a tier without the EXP it pays.
func evaluate(txn *gorm.DB, event activity.Event, criteria []string) (int, error) {
	user, err := upsertUser(txn, event)
	if err != nil {
		return 0, err
	}

	var awarded int

	for _, criteriaKey := range criteria {
		count, incErr := incrementCounter(txn, user.ID, criteriaKey, event.Weight())
		if incErr != nil {
			return 0, incErr
		}

		earned, awardErr := awardReachedTiers(txn, user.ID, criteriaKey, count, event.OccurredAt)
		if awardErr != nil {
			return 0, awardErr
		}

		awarded += earned
	}

	earned, err := evaluateDays(txn, user.ID, event.OccurredAt)
	if err != nil {
		return 0, err
	}

	awarded += earned

	if awarded == 0 {
		return 0, nil
	}

	_, err = recomputeExp(txn, user.ID)
	if err != nil {
		return 0, err
	}

	return awarded, nil
}

// evaluateDays records the day the activity happened on and, only when that
// changed something, recomputes the criteria derived from the user's
// activity-day set. It returns how many tiers were newly awarded.
//
// Any activity counts towards these, whatever kind it was: a comment and a
// pipeline both mean the user was there that day. Skipping the recompute
// when the day was already known is what keeps this from re-reading a
// decade of days for every event in a busy afternoon.
func evaluateDays(txn *gorm.DB, userID int64, occurredAt time.Time) (int, error) {
	changed, err := recordActivityDay(txn, userID, occurredAt)
	if err != nil || !changed {
		return 0, err
	}

	totals, err := computeDayTotals(txn, userID)
	if err != nil {
		return 0, err
	}

	var awarded int

	for _, criteriaKey := range dayCriteria {
		value := totals.value(criteriaKey)

		err = setCounter(txn, userID, criteriaKey, value)
		if err != nil {
			return 0, err
		}

		earned, awardErr := awardReachedTiers(txn, userID, criteriaKey, value, occurredAt)
		if awardErr != nil {
			return 0, awardErr
		}

		awarded += earned
	}

	return awarded, nil
}

// recordProcessed inserts event's dedup key into the processed-event log,
// reporting whether it was new. A false return means the event was already
// counted and must not be counted again.
//
// The lookup-then-insert is what makes a single writer cheap; the unique
// index on event_id is what makes concurrent writers correct. Two callers
// racing on the same event leave one of them with a constraint violation,
// which is returned (and rolls the transaction back) rather than swallowed:
// on retry the loser sees the row and skips, which is the same outcome
// without guessing at dialect-specific constraint error shapes.
func recordProcessed(txn *gorm.DB, event activity.Event) (bool, error) {
	var seen int64

	err := txn.Model(&db.ProcessedEvent{}).Where("event_id = ?", event.DedupKey).Count(&seen).Error
	if err != nil {
		return false, fmt.Errorf("failed to check whether event %q was already processed: %w", event.DedupKey, err)
	}

	if seen > 0 {
		return false, nil
	}

	record := db.ProcessedEvent{
		EventID:     event.DedupKey,
		EventType:   string(event.Kind),
		ProcessedAt: time.Now().UTC(),
	}

	err = txn.Create(&record).Error
	if err != nil {
		return false, fmt.Errorf("failed to record event %q as processed: %w", event.DedupKey, err)
	}

	return true, nil
}

// upsertUser returns the local row for the event's actor, creating it on
// first sight and keeping its username in step with GitLab's when it was
// renamed since.
func upsertUser(txn *gorm.DB, event activity.Event) (*db.User, error) {
	user := db.User{GitLabUserID: event.ActorID, Username: event.ActorUsername}

	err := txn.Where(&db.User{GitLabUserID: event.ActorID}).
		Attrs(&db.User{Username: event.ActorUsername}).
		FirstOrCreate(&user).Error
	if err != nil {
		return nil, fmt.Errorf("failed to resolve user %d: %w", event.ActorID, err)
	}

	if event.ActorUsername != "" && user.Username != event.ActorUsername {
		user.Username = event.ActorUsername

		err = txn.Model(&user).Update("username", event.ActorUsername).Error
		if err != nil {
			return nil, fmt.Errorf("failed to update username for user %d: %w", event.ActorID, err)
		}
	}

	return &user, nil
}

// incrementCounter adds weight to a user's running total for criteriaKey,
// creating the counter on first use, and returns the new total.
func incrementCounter(txn *gorm.DB, userID int64, criteriaKey string, weight int64) (int64, error) {
	counter := db.ProgressCounter{UserID: userID, CriteriaKey: criteriaKey}

	err := txn.Where(&db.ProgressCounter{UserID: userID, CriteriaKey: criteriaKey}).
		FirstOrCreate(&counter).Error
	if err != nil {
		return 0, fmt.Errorf("failed to resolve %q counter for user %d: %w", criteriaKey, userID, err)
	}

	counter.Count += weight

	err = txn.Model(&counter).Update("count", counter.Count).Error
	if err != nil {
		return 0, fmt.Errorf("failed to increment %q counter for user %d: %w", criteriaKey, userID, err)
	}

	return counter.Count, nil
}

// setCounter overwrites a user's total for criteriaKey, for the criteria
// that are recomputed from a set rather than accumulated event by event
// (see dayCriteria). Storing them in the same table as the cumulative ones
// means award evaluation, and anything that later reports progress, doesn't
// have to care which sort a criteria is.
func setCounter(txn *gorm.DB, userID int64, criteriaKey string, value int64) error {
	counter := db.ProgressCounter{UserID: userID, CriteriaKey: criteriaKey}

	err := txn.Where(&db.ProgressCounter{UserID: userID, CriteriaKey: criteriaKey}).
		FirstOrCreate(&counter).Error
	if err != nil {
		return fmt.Errorf("failed to resolve %q counter for user %d: %w", criteriaKey, userID, err)
	}

	if counter.Count == value {
		return nil
	}

	err = txn.Model(&counter).Update("count", value).Error
	if err != nil {
		return fmt.Errorf("failed to set %q counter for user %d: %w", criteriaKey, userID, err)
	}

	return nil
}

// awardReachedTiers records an award for every criteriaKey tier whose
// threshold count now reaches and the user doesn't already hold, returning
// how many were newly recorded.
//
// Every reached tier is awarded, not just the highest: the catalog stacks
// tiers (Committer I, II, ...), so a single event crossing two thresholds
// at once, or a first run over a user's whole history, must yield both.
func awardReachedTiers(txn *gorm.DB, userID int64, criteriaKey string, count int64, occurredAt time.Time) (int, error) {
	var definitions []db.AchievementDefinition

	err := txn.Where("criteria_key = ? AND threshold <= ?", criteriaKey, count).Find(&definitions).Error
	if err != nil {
		return 0, fmt.Errorf("failed to load %q achievement definitions: %w", criteriaKey, err)
	}

	var awarded int

	for _, definition := range definitions {
		var held int64

		err = txn.Model(&db.Award{}).
			Where("user_id = ? AND achievement_definition_id = ?", userID, definition.ID).
			Count(&held).Error
		if err != nil {
			return awarded, fmt.Errorf("failed to check award %d for user %d: %w", definition.ID, userID, err)
		}

		if held > 0 {
			continue
		}

		err = txn.Create(&db.Award{
			UserID:                  userID,
			AchievementDefinitionID: definition.ID,
			Status:                  db.AwardStatusPending,
			AwardedAt:               occurredAt,
		}).Error
		if err != nil {
			return awarded, fmt.Errorf("failed to record award %d for user %d: %w", definition.ID, userID, err)
		}

		awarded++
	}

	return awarded, nil
}
