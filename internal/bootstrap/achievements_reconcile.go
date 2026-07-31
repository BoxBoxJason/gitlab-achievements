package bootstrap

import (
	"context"
	"errors"
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// ReconcileAchievements re-checks that every catalog entry's achievement
// still exists on GitLab, recreating any that were deleted since the last
// check, and pushes any Name/Description drift found in GitLab's own
// current record of the achievement, not just drift against the local
// row, back in line with the catalog.
//
// Existence and current state are checked against a single ListAchievements
// call over namespaceFullPath rather than one probe request per catalog
// entry, so cost stays roughly constant as the catalog grows instead of
// scaling with entry count.
func ReconcileAchievements(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, namespaceFullPath string, entries []catalog.Entry) (AchievementsReport, error) {
	var report AchievementsReport

	live, err := listAchievementsByID(ctx, write, namespaceFullPath)
	if err != nil {
		return report, err
	}

	for _, entry := range entries {
		entryErr := reconcileEntry(ctx, write, conn, namespaceID, live, entry, &report)
		if entryErr != nil {
			return report, entryErr
		}
	}

	return report, nil
}

// reconcileEntry reconciles a single catalog entry against its local row
// (creating it if missing, recreating it if GitLab no longer has it, or
// pushing drift found against GitLab's live record otherwise), tallying the
// outcome into report.
func reconcileEntry(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, live map[int64]*gitlab.Achievement, entry catalog.Entry, report *AchievementsReport) error {
	var existing db.AchievementDefinition

	lookupErr := conn.Where("criteria_key = ? AND tier = ?", entry.CriteriaKey, entry.Tier).First(&existing).Error

	switch {
	case errors.Is(lookupErr, gorm.ErrRecordNotFound):
		createErr := createAchievement(ctx, write, conn, namespaceID, entry)
		if createErr != nil {
			return createErr
		}

		report.Created++
	case lookupErr != nil:
		return fmt.Errorf("failed to look up achievement definition for %s tier %d: %w", entry.CriteriaKey, entry.Tier, lookupErr)
	default:
		liveAchievement, stillExists := live[existing.GitLabAchievementID]
		if !stillExists {
			recreateErr := recreateAchievement(ctx, write, conn, namespaceID, &existing, entry)
			if recreateErr != nil {
				return recreateErr
			}

			report.Recreated++

			return nil
		}

		changed, err := reconcileAchievement(ctx, write, conn, liveAchievement, &existing, entry)
		if err != nil {
			return err
		}

		if changed {
			report.Updated++
		} else {
			report.Unchanged++
		}
	}

	return nil
}

// listAchievementsByID fetches every achievement GitLab currently has
// recorded for namespaceFullPath, keyed by their ID, so reconcileEntry can
// both check existence and compare each entry's current Name/Description
// against the catalog in O(1) instead of one probe request per entry.
func listAchievementsByID(ctx context.Context, write achievementWriter, namespaceFullPath string) (map[int64]*gitlab.Achievement, error) {
	byID := make(map[int64]*gitlab.Achievement)

	for achievement, err := range write.ListAchievements(namespaceFullPath, nil, gitlab.WithContext(ctx)) {
		if err != nil {
			return nil, fmt.Errorf("failed to list existing achievements in %q: %w", namespaceFullPath, err)
		}

		byID[achievement.ID] = achievement
	}

	return byID, nil
}

// recreateAchievement creates a brand-new GitLab achievement for entry and
// overwrites existing's row in place with the new ID, rather than inserting
// a duplicate row.
func recreateAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, existing *db.AchievementDefinition, entry catalog.Entry) error {
	avatar, closeAvatar, err := openAvatarUpload(entry)
	if err != nil {
		return err
	}
	defer closeAvatar()

	achievement, err := write.CreateAchievement(namespaceID, &gitlab.CreateAchievementOptions{
		Name:        &entry.Name,
		Description: &entry.Description,
		Avatar:      avatar,
	}, gitlab.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to recreate deleted achievement %q: %w", entry.Name, err)
	}

	existing.GitLabAchievementID = achievement.ID
	existing.Name = entry.Name
	existing.Description = entry.Description
	existing.Threshold = entry.Threshold
	existing.ExpReward = entry.Exp
	existing.AvatarPath = entry.AvatarPath

	err = conn.Save(existing).Error
	if err != nil {
		return fmt.Errorf("failed to persist recreated achievement definition %q: %w", entry.Name, err)
	}

	return nil
}
