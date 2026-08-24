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

// ReconcileAchievements makes GitLab's achievements in namespaceFullPath
// match the catalog, and the local database match both.
//
// Every catalog entry ends the pass bound to exactly one GitLab
// achievement, whichever way it has to get there: adopted from what GitLab
// already holds under the entry's name, created, recreated when the one the
// database recorded has since been deleted, or updated in place when its
// Name/Description/avatar have drifted from the catalog.
//
// This is what both bootstrap and the periodic sweep run, so a first
// install and an hourly re-check are the same code path with the same
// outcome. It is safe against a namespace this app has already populated,
// and against a database that knows nothing about one.
//
// GitLab's current state is read with a single ListAchievements call over
// namespaceFullPath rather than one probe per catalog entry, so cost stays
// roughly constant as the catalog grows instead of scaling with entry
// count.
func ReconcileAchievements(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, namespaceFullPath string, entries []catalog.Entry) (AchievementsReport, error) {
	var report AchievementsReport

	live, err := listAchievements(ctx, write, namespaceFullPath)
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

// reconcileEntry reconciles a single catalog entry, tallying the outcome
// into report. Which of the two halves below it takes turns on whether the
// database already has a row for the entry, since that row is the only
// place the entry's GitLab achievement ID is ever recorded.
func reconcileEntry(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, live liveAchievements, entry catalog.Entry, report *AchievementsReport) error {
	var existing db.AchievementDefinition

	lookupErr := conn.Where("criteria_key = ? AND tier = ?", entry.CriteriaKey, entry.Tier).First(&existing).Error

	switch {
	case errors.Is(lookupErr, gorm.ErrRecordNotFound):
		return claimAchievement(ctx, write, conn, namespaceID, live, &existing, entry, report)
	case lookupErr != nil:
		return fmt.Errorf("failed to look up achievement definition for %s tier %d: %w", entry.CriteriaKey, entry.Tier, lookupErr)
	default:
		return resyncAchievement(ctx, write, conn, namespaceID, live, &existing, entry, report)
	}
}

// claimAchievement gives entry a GitLab achievement when the database holds
// no row for it, and writes the row recording which one.
//
// GitLab already holding an achievement under the entry's name is not a
// conflict, it is this app's own earlier work seen from a database that has
// lost sight of it: a store rebuilt from nothing, restored from a backup
// older than the achievements it describes, or one whose row was deleted by
// hand. Creating a second achievement is not an option there anyway, since
// GitLab rejects a duplicate name outright, so the one already there is
// adopted and pulled in line with the catalog instead.
func claimAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, live liveAchievements, existing *db.AchievementDefinition, entry catalog.Entry, report *AchievementsReport) error {
	adoptable, held := live.byName[entry.Name]
	if !held {
		err := createAchievement(ctx, write, conn, namespaceID, entry)
		if err != nil {
			return err
		}

		report.Created++

		return nil
	}

	err := adoptAchievement(ctx, write, conn, adoptable, existing, entry)
	if err != nil {
		return err
	}

	report.Adopted++

	return nil
}

// resyncAchievement brings the achievement an existing row already points
// at back in line with entry.
//
// The row's recorded achievement being gone from GitLab does not
// necessarily mean it has to be made again: a namespace still holding one
// under the entry's name has an achievement whose ID this row has simply
// lost track of, which is what a database restored from a backup taken
// before the achievements were last recreated looks like. Adopting it keeps
// every award already hanging off it, where recreating would strand them,
// and would fail on the duplicate name before it got that far.
func resyncAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, live liveAchievements, existing *db.AchievementDefinition, entry catalog.Entry, report *AchievementsReport) error {
	recorded, stillExists := live.byID[existing.GitLabAchievementID]
	if stillExists {
		changed, err := reconcileAchievement(ctx, write, conn, recorded, existing, entry)
		if err != nil {
			return err
		}

		if changed {
			report.Updated++
		} else {
			report.Unchanged++
		}

		return nil
	}

	adoptable, held := live.byName[entry.Name]
	if held {
		err := adoptAchievement(ctx, write, conn, adoptable, existing, entry)
		if err != nil {
			return err
		}

		report.Adopted++

		return nil
	}

	err := recreateAchievement(ctx, write, conn, namespaceID, existing, entry)
	if err != nil {
		return err
	}

	report.Recreated++

	return nil
}

// adoptAchievement binds existing to the achievement GitLab already holds
// for entry, then reconciles it, so an adopted achievement whose
// description or avatar has drifted from the catalog is pushed back in line
// in the same pass rather than left for the next one.
//
// existing is either a zero-valued row about to be inserted or one whose
// recorded ID went stale; conn.Save covers both, inserting when the row
// carries no primary key yet and updating in place otherwise. Either way
// the fields GitLab owns are filled from live rather than from entry,
// because the row records what GitLab actually holds, and the difference
// between the two is exactly what the reconciliation below has to still be
// able to see in order to push it.
func adoptAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, live *gitlab.Achievement, existing *db.AchievementDefinition, entry catalog.Entry) error {
	description := ""
	if live.Description != nil {
		description = *live.Description
	}

	// An avatar can only be seen as present or absent, never compared
	// against the catalog's asset, so an achievement GitLab already shows
	// one for is taken to be showing the entry's. One with no avatar
	// records no path, which is what makes an entry that expects an avatar
	// read as drifted and get one uploaded.
	avatarPath := ""
	if live.AvatarURL != nil {
		avatarPath = entry.AvatarPath
	}

	existing.GitLabAchievementID = live.ID
	existing.CriteriaKey = entry.CriteriaKey
	existing.Tier = entry.Tier
	existing.Threshold = entry.Threshold
	existing.ExpReward = entry.Exp
	existing.Name = live.Name
	existing.Description = description
	existing.AvatarPath = avatarPath

	err := conn.Save(existing).Error
	if err != nil {
		return fmt.Errorf("failed to persist adopted achievement definition %q: %w", entry.Name, err)
	}

	_, err = reconcileAchievement(ctx, write, conn, live, existing, entry)
	if err != nil {
		return err
	}

	return nil
}

// liveAchievements is what GitLab currently holds for the achievements
// namespace, indexed both of the ways a catalog entry can be matched to it.
type liveAchievements struct {
	// byID answers "is the achievement this row recorded still there, and
	// what does it say now".
	byID map[int64]*gitlab.Achievement
	// byName answers "has this entry got an achievement already, whatever
	// the database believes". Names are matched exactly: GitLab holds a
	// namespace's achievement names unique, and the ones in this namespace
	// are this app's own, written from the same catalog.
	byName map[string]*gitlab.Achievement
}

// listAchievements fetches every achievement GitLab currently has recorded
// for namespaceFullPath, so each catalog entry can be matched against them
// in O(1) instead of one probe request per entry.
func listAchievements(ctx context.Context, write achievementWriter, namespaceFullPath string) (liveAchievements, error) {
	live := liveAchievements{
		byID:   make(map[int64]*gitlab.Achievement),
		byName: make(map[string]*gitlab.Achievement),
	}

	for achievement, err := range write.ListAchievements(namespaceFullPath, nil, gitlab.WithContext(ctx)) {
		if err != nil {
			return liveAchievements{}, fmt.Errorf("failed to list existing achievements in %q: %w", namespaceFullPath, err)
		}

		live.byID[achievement.ID] = achievement
		live.byName[achievement.Name] = achievement
	}

	return live, nil
}

// recreateAchievement creates a brand-new GitLab achievement for entry and
// overwrites existing's row in place with the new ID, rather than inserting
// a duplicate row.
func recreateAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, existing *db.AchievementDefinition, entry catalog.Entry) error {
	avatar, err := avatarUpload(entry)
	if err != nil {
		return err
	}

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
