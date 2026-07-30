package engine

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// expSumQuery totals the EXP a set of awards is worth by joining each award
// to the tier it was earned for. Every award counts, whatever its delivery
// status: an award is EXP the moment the engine decides the tier was
// earned, and whether GitLab has accepted the corresponding mutation yet
// says nothing about whether the user did the work.
const expSumQuery = "COALESCE(SUM(achievement_definitions.exp_reward), 0)"

// expJoin attaches each award to its achievement definition, which is where
// the per-tier EXP reward lives.
const expJoin = "JOIN achievement_definitions ON achievement_definitions.id = awards.achievement_definition_id"

// expSumSubquery is the same total as a correlated subquery, for computing
// a user's EXP inside the UPDATE that stores it rather than reading it out
// and writing it back. Several workers evaluate activity at once, so two
// events for one user can be in flight together; letting the database do
// the sum at write time keeps one worker from overwriting the other's
// award with a total it read before that award existed.
const expSumSubquery = "(SELECT COALESCE(SUM(achievement_definitions.exp_reward), 0) FROM awards " +
	expJoin + " WHERE awards.user_id = ?)"

// recomputeExp brings a user's stored EXP total back in line with the
// awards they hold, returning the total.
//
// It re-derives the whole sum rather than adding the newly earned tier's
// reward to the running total, which costs one aggregate query per event
// that awards something and buys three things a forward-only counter can't
// give:
//
//   - The historical backfill awards tiers in whatever order it walks
//     projects in, and may award several at once. A sum doesn't care.
//   - Retuning the catalog's EXP values between releases changes what
//     already-held tiers are worth. Only a recompute picks that up; see
//     RecomputeAll, which bootstrap runs when a definition's reward drifts.
//   - It is the same code path in the engine and in a repair run, so
//     "recomputed" and "accumulated" can't disagree about a user's total.
//
// Superseded tiers stay counted. Hiding a lower tier from a user's GitLab
// profile (the top-tier-only display) doesn't unearn it, so nothing here
// filters on visibility.
func recomputeExp(txn *gorm.DB, userID int64) (int64, error) {
	err := txn.Model(&db.User{}).
		Where("id = ?", userID).
		Update("exp_total", gorm.Expr(expSumSubquery, userID)).Error
	if err != nil {
		return 0, fmt.Errorf("failed to persist EXP total for user %d: %w", userID, err)
	}

	var total int64

	err = txn.Model(&db.User{}).Where("id = ?", userID).Select("exp_total").Scan(&total).Error
	if err != nil {
		return 0, fmt.Errorf("failed to read back EXP total for user %d: %w", userID, err)
	}

	return total, nil
}

// RecomputeEXP re-derives one user's EXP total from the awards they hold
// and persists it, returning the total. userID is the local db.User ID, not
// the GitLab one.
//
// Callers on the awarding path don't need this: Process already keeps the
// total in step, inside the same transaction that records the award. It is
// for the paths that change what an already-held tier is worth, and for
// repairing a total by hand.
func RecomputeEXP(ctx context.Context, conn *gorm.DB, userID int64) (int64, error) {
	return recomputeExp(conn.WithContext(ctx), userID)
}

// RecomputeAll re-derives every user's EXP total from the awards they hold,
// returning how many users' stored totals were wrong and have been
// corrected.
//
// This is what makes a catalog retune safe: the EXP a tier is worth is
// stored per definition, so raising or lowering it leaves every user who
// already holds that tier carrying a stale total until their next award.
// Bootstrap runs this after reconciling definitions, so the correction
// lands with the retune rather than whenever the user next does something.
//
// A user with no awards is included, and settles at zero: that is how a
// total left over from a since-deleted award gets cleared.
func RecomputeAll(ctx context.Context, conn *gorm.DB) (int, error) {
	txn := conn.WithContext(ctx)

	earned, err := earnedExpByUser(txn)
	if err != nil {
		return 0, err
	}

	var users []db.User

	err = txn.Find(&users).Error
	if err != nil {
		return 0, fmt.Errorf("failed to load users for EXP recomputation: %w", err)
	}

	var corrected int

	for _, user := range users {
		total := earned[user.ID]
		if user.ExpTotal == total {
			continue
		}

		err = txn.Model(&db.User{}).Where("id = ?", user.ID).Update("exp_total", total).Error
		if err != nil {
			return corrected, fmt.Errorf("failed to persist EXP total for user %d: %w", user.ID, err)
		}

		corrected++
	}

	return corrected, nil
}

// earnedExpByUser totals every user's EXP in one grouped query, so a
// full recomputation costs two reads plus one write per user whose stored
// total was actually wrong, rather than a query per user.
func earnedExpByUser(txn *gorm.DB) (map[int64]int64, error) {
	var rows []struct {
		UserID int64
		Total  int64
	}

	err := txn.Model(&db.Award{}).
		Joins(expJoin).
		Group("awards.user_id").
		Select("awards.user_id AS user_id, " + expSumQuery + " AS total").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to total EXP per user: %w", err)
	}

	totals := make(map[int64]int64, len(rows))
	for _, row := range rows {
		totals[row.UserID] = row.Total
	}

	return totals, nil
}
