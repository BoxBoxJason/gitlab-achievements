package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"path"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// achievementWriter is the subset of gitlabclient.WriteClient achievement
// synchronization and awarding needs.
type achievementWriter interface {
	CreateAchievement(namespaceID int64, opt *gitlab.CreateAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error)
	UpdateAchievement(achievementID int64, opt *gitlab.UpdateAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error)
	AwardAchievement(achievementID, userID int64, opt *gitlab.AwardAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error)
	ListAchievements(fullPath string, opt *gitlab.ListAchievementsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Achievement, error]
}

// AchievementsReport summarizes what syncAchievements or ReconcileAchievements did.
type AchievementsReport struct {
	Created   int
	Updated   int
	Unchanged int
	// Recreated counts achievements ReconcileAchievements found deleted on
	// GitLab's side and had to create again under a new GitLab ID.
	Recreated int
}

// syncAchievements idempotently creates or updates every catalog entry as a
// GitLab achievement in namespaceID.
//
// The Achievements GraphQL API has no way to list or look up existing
// achievements, so the local database (conn), keyed by CriteriaKey+Tier,
// is the source of truth for which catalog entries already have a GitLab
// achievement and what was last pushed for it. A catalog entry with no
// matching row is created; one whose stored Name/Description/Threshold/
// AvatarPath drifted from the catalog is pushed via achievementsUpdate and
// the row updated to match.
func syncAchievements(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, entries []catalog.Entry) (AchievementsReport, error) {
	var report AchievementsReport

	for _, entry := range entries {
		var existing db.AchievementDefinition

		lookupErr := conn.Where("criteria_key = ? AND tier = ?", entry.CriteriaKey, entry.Tier).First(&existing).Error

		switch {
		case errors.Is(lookupErr, gorm.ErrRecordNotFound):
			createErr := createAchievement(ctx, write, conn, namespaceID, entry)
			if createErr != nil {
				return report, createErr
			}

			report.Created++
		case lookupErr != nil:
			return report, fmt.Errorf("failed to look up achievement definition for %s tier %d: %w", entry.CriteriaKey, entry.Tier, lookupErr)
		default:
			changed, reconcileErr := reconcileAchievement(ctx, write, conn, nil, &existing, entry)
			if reconcileErr != nil {
				return report, reconcileErr
			}

			if changed {
				report.Updated++
			} else {
				report.Unchanged++
			}
		}
	}

	return report, nil
}

func createAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, entry catalog.Entry) error {
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
		return fmt.Errorf("failed to create achievement %q: %w", entry.Name, err)
	}

	def := db.AchievementDefinition{
		GitLabAchievementID: achievement.ID,
		CriteriaKey:         entry.CriteriaKey,
		Name:                entry.Name,
		Description:         entry.Description,
		AvatarPath:          entry.AvatarPath,
		Tier:                entry.Tier,
		Threshold:           entry.Threshold,
		ExpReward:           entry.Exp,
	}

	err = conn.Create(&def).Error
	if err != nil {
		return fmt.Errorf("failed to persist achievement definition %q: %w", entry.Name, err)
	}

	return nil
}

// openAvatarUpload opens entry's avatar asset, if it has one, ready to
// attach to a Create/UpdateAchievement call. The caller must call the
// returned close func once the request has been sent, whether or not
// entry has an avatar, it is a safe no-op when it doesn't. Any error
// closing the underlying asset is deliberately swallowed: it carries no
// actionable information once the upload has already been sent.
func openAvatarUpload(entry catalog.Entry) (*gitlab.GraphQLUpload, func(), error) {
	noop := func() {}

	if !entry.HasAvatar() {
		return nil, noop, nil
	}

	file, err := entry.Avatar()
	if err != nil {
		return nil, noop, fmt.Errorf("failed to open avatar for achievement %q: %w", entry.Name, err)
	}

	upload := &gitlab.GraphQLUpload{
		Content:     file,
		Filename:    path.Base(entry.AvatarPath),
		ContentType: "image/png",
	}

	return upload, func() { _ = file.Close() }, nil
}

// achievementDrift tracks which parts of an achievement definition have
// drifted from the catalog and need pushing back to GitLab.
type achievementDrift struct {
	name        bool
	description bool
	avatar      bool
}

func (d achievementDrift) any() bool {
	return d.name || d.description || d.avatar
}

// detectAchievementDrift compares entry against either live (GitLab's own
// current record, when available) or existing (the local row, as a
// fallback) to decide what needs pushing back to GitLab.
//
// live, when non-nil, is what GitLab's ListAchievements call actually
// returned for this achievement and is treated as ground truth. This is
// what lets ReconcileAchievements catch an achievement that was hand-edited
// on GitLab even though the local row still matches the catalog.
// Bootstrap-time syncAchievements has no cheap way to fetch live state per
// entry, so it passes nil and falls back to comparing the local row
// against entry, same as before.
//
// Avatar drift detection is presence-only: GitLab's API exposes an
// achievement's AvatarURL but not its bytes, so there is no cheap way to
// diff live image content against the catalog source. When live is given,
// an entry that expects an avatar but whose live AvatarURL is nil (cleared
// out from under it) is treated as drifted and re-uploaded; an achievement
// GitLab reports an avatar for that the catalog doesn't expect is left
// alone, since there is no supported way to clear an avatar via this API.
func detectAchievementDrift(live *gitlab.Achievement, existing *db.AchievementDefinition, entry catalog.Entry) achievementDrift {
	if live == nil {
		return achievementDrift{
			name:        existing.Name != entry.Name,
			description: existing.Description != entry.Description,
			avatar:      existing.AvatarPath != entry.AvatarPath,
		}
	}

	liveDescription := ""
	if live.Description != nil {
		liveDescription = *live.Description
	}

	return achievementDrift{
		name:        live.Name != entry.Name,
		description: liveDescription != entry.Description,
		avatar:      entry.AvatarPath != "" && live.AvatarURL == nil,
	}
}

// pushAchievementUpdate sends entry's current Name/Description to GitLab,
// attaching a freshly opened avatar upload when drift.avatar says one is
// needed.
func pushAchievementUpdate(ctx context.Context, write achievementWriter, achievementID int64, entry catalog.Entry, drift achievementDrift) error {
	opt := &gitlab.UpdateAchievementOptions{
		Name:        &entry.Name,
		Description: &entry.Description,
	}

	if drift.avatar {
		avatar, closeAvatar, err := openAvatarUpload(entry)
		if err != nil {
			return err
		}
		defer closeAvatar()

		opt.Avatar = avatar
	}

	_, err := write.UpdateAchievement(achievementID, opt, gitlab.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to update achievement %q: %w", entry.Name, err)
	}

	return nil
}

// reconcileAchievement pushes any Name/Description/avatar drift to GitLab
// (see detectAchievementDrift) and keeps existing (the local row) in sync
// with entry.
//
// Threshold and EXP drift are local-only: GitLab stores neither, so a
// retuned curve updates this row and makes no API call. Retuned EXP does
// mean every user already holding the tier has a stale total, which is what
// the RecomputeAll call on the callers' side repairs.
func reconcileAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, live *gitlab.Achievement, existing *db.AchievementDefinition, entry catalog.Entry) (bool, error) {
	drift := detectAchievementDrift(live, existing, entry)

	if !drift.any() && existing.Threshold == entry.Threshold && existing.ExpReward == entry.Exp {
		return false, nil
	}

	if drift.any() {
		err := pushAchievementUpdate(ctx, write, existing.GitLabAchievementID, entry, drift)
		if err != nil {
			return false, err
		}
	}

	existing.Name = entry.Name
	existing.Description = entry.Description
	existing.Threshold = entry.Threshold
	existing.ExpReward = entry.Exp
	existing.AvatarPath = entry.AvatarPath

	err := conn.Save(existing).Error
	if err != nil {
		return false, fmt.Errorf("failed to persist updated achievement definition %q: %w", entry.Name, err)
	}

	return true, nil
}
