package bootstrap

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// AwardsReport summarizes what ReconcileAwards did.
type AwardsReport struct {
	Confirmed int
	Failed    int
}

// ReconcileAwards retries every locally recorded Award that GitLab hasn't
// yet confirmed (db.AwardStatusPending) or that previously failed
// (db.AwardStatusFailed).
//
// Nothing in this codebase creates Award rows yet, backfill and webhook
// event ingestion, which will drive actual awarding, aren't implemented,
// so today this is forward-compatible infrastructure: once that logic
// lands, any award GitLab didn't accept on the first attempt gets retried
// here on every reconciliation pass until it succeeds, with no retry cap or
// backoff (consistent with reconciliation elsewhere in this package, which
// just keeps healing drift on its next scheduled pass).
func ReconcileAwards(write achievementWriter, conn *gorm.DB) (AwardsReport, error) {
	var report AwardsReport

	var pending []db.Award

	err := conn.
		Preload("User").
		Preload("AchievementDefinition").
		Where("status <> ?", db.AwardStatusAccepted).
		Find(&pending).Error
	if err != nil {
		return report, fmt.Errorf("failed to load unconfirmed awards: %w", err)
	}

	for _, award := range pending {
		confirmed, retryErr := retryAward(write, conn, &award)
		if retryErr != nil {
			return report, retryErr
		}

		if confirmed {
			report.Confirmed++
		} else {
			report.Failed++
		}
	}

	return report, nil
}

// retryAward attempts to (re-)award award.AchievementDefinition to
// award.User, persisting the resulting status either way. Only a local
// database error is returned to the caller; a rejected/failed GitLab call
// is recorded as db.AwardStatusFailed and left for the next reconciliation
// pass to retry.
func retryAward(write achievementWriter, conn *gorm.DB, award *db.Award) (bool, error) {
	_, awardErr := write.AwardAchievement(award.AchievementDefinition.GitLabAchievementID, award.User.GitLabUserID, nil)
	if awardErr != nil {
		award.Status = db.AwardStatusFailed
	} else {
		award.Status = db.AwardStatusAccepted
	}

	saveErr := conn.Model(award).Update("status", award.Status).Error
	if saveErr != nil {
		return false, fmt.Errorf("failed to persist award %d status: %w", award.ID, saveErr)
	}

	return awardErr == nil, nil
}
