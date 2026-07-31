package engine

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// expSumExpression totals a set of tiers' EXP rewards.
//
// It is the one piece of SQL written out by hand here, because an aggregate
// has no builder in gorm. It names no table and no dialect-specific
// construct: COALESCE and SUM are core SQL, and the column is unqualified,
// so the statement gorm builds around it is what adapts to the DBMS.
const expSumExpression = "COALESCE(SUM(exp_reward), 0)"

// heldExp builds the query totalling what the tiers a user holds are worth.
//
// It sums over the definitions rather than joining awards to them, so both
// halves stay ordinary gorm model queries: the table names, their quoting,
// and the placeholder syntax all come from the model and the dialect rather
// than from a join clause written for one DBMS's spelling. An award is
// unique per user and definition, so selecting the definitions a user has
// been awarded counts each tier exactly once.
//
// Every award counts, whatever its delivery status: an award is EXP the
// moment the engine decides the tier was earned, and whether GitLab has
// accepted the corresponding mutation yet says nothing about whether the
// user did the work.
//
// The result is a query, not a number, so callers can either run it or hand
// it to gorm as a subquery to be evaluated inside another statement.
func heldExp(txn *gorm.DB, userID int64) *gorm.DB {
	held := txn.Model(&db.Award{}).
		Select("achievement_definition_id").
		Where(&db.Award{UserID: userID})

	return txn.Model(&db.AchievementDefinition{}).
		Select(expSumExpression).
		Where("id IN (?)", held)
}

// earnedExp runs that total for one user, without touching what is stored.
func earnedExp(txn *gorm.DB, userID int64) (int64, error) {
	var total int64

	err := heldExp(txn, userID).Scan(&total).Error
	if err != nil {
		return 0, fmt.Errorf("failed to total EXP for user %d: %w", userID, err)
	}

	return total, nil
}

// recomputeExp brings a user's stored EXP total back in line with the
// awards they hold, returning the total.
//
// It re-derives the whole sum rather than adding the newly earned tier's
// reward to the running total, which costs one aggregate per event that
// awards something and buys three things a forward-only counter can't give:
//
//   - The historical backfill awards tiers in whatever order it walks
//     projects in, and may award several at once. A sum doesn't care.
//   - Retuning the catalog's EXP values between releases changes what
//     already-held tiers are worth. Only a recompute picks that up; see
//     RecomputeAll, which bootstrap runs when a definition's reward drifts.
//   - It is the same code path in the engine and in a repair run, so
//     "recomputed" and "accumulated" can't disagree about a user's total.
//
// The sum is handed to gorm as a subquery so it is evaluated by the
// database as part of the UPDATE, not read out and written back. Several
// workers evaluate activity at once, so two events for one user can be in
// flight together, and a total read before the other worker's award existed
// would be stale by the time it was written.
//
// Superseded tiers stay counted. Hiding a lower tier from a user's GitLab
// profile (the top-tier-only display) doesn't unearn it, so nothing here
// filters on visibility.
func recomputeExp(txn *gorm.DB, userID int64) (int64, error) {
	err := txn.Model(&db.User{}).
		Where(&db.User{ID: userID}).
		Update("exp_total", heldExp(txn, userID)).Error
	if err != nil {
		return 0, fmt.Errorf("failed to persist EXP total for user %d: %w", userID, err)
	}

	var total int64

	err = txn.Model(&db.User{}).Where(&db.User{ID: userID}).Select("exp_total").Scan(&total).Error
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
// total left over from a since-deleted award gets cleared. Users whose
// total is already right are left untouched rather than rewritten, so a
// sweep over an instance where one tier was retuned doesn't churn every row
// in the table.
func RecomputeAll(ctx context.Context, conn *gorm.DB) (int, error) {
	txn := conn.WithContext(ctx)

	var users []db.User

	err := txn.Find(&users).Error
	if err != nil {
		return 0, fmt.Errorf("failed to load users for EXP recomputation: %w", err)
	}

	var corrected int

	for _, user := range users {
		total, earnedErr := earnedExp(txn, user.ID)
		if earnedErr != nil {
			return corrected, earnedErr
		}

		if user.ExpTotal == total {
			continue
		}

		// Deliberately re-derived by recomputeExp rather than written from
		// the total just read: one write path, and the value stored is the
		// one the database computes at write time.
		_, err = recomputeExp(txn, user.ID)
		if err != nil {
			return corrected, err
		}

		corrected++
	}

	return corrected, nil
}
