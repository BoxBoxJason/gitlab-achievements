package bootstrap

import (
	"context"
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
	RevokeAchievement(userAchievementID int64, options ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error)
	ListAchievements(fullPath string, opt *gitlab.ListAchievementsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Achievement, error]
	ListUserAchievements(username string, opt *gitlab.ListUserAchievementsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.UserAchievement, error]
}

// AchievementsReport summarizes what ReconcileAchievements did.
type AchievementsReport struct {
	Created   int
	Updated   int
	Unchanged int
	// Recreated counts achievements found deleted on GitLab's side, with
	// nothing left under their name to adopt, and created again under a new
	// GitLab ID.
	Recreated int
	// Adopted counts catalog entries bound to an achievement GitLab already
	// held under the entry's name instead of created a second time. It is
	// what a database rebuilt from nothing, or restored from a backup older
	// than the achievements it describes, reports on its first pass.
	Adopted int
}

func createAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, entry catalog.Entry) error {
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

// avatarUpload reads entry's avatar asset, if it has one, into an upload
// ready to attach to a Create/UpdateAchievement call. It returns a nil
// upload when entry has no avatar, which the API takes as "leave the
// avatar alone".
//
// The upload holds its own copy of the image, so the asset is closed here
// rather than by the caller once the request has been sent. Any error
// closing it is deliberately swallowed: the bytes are already in hand and
// the failure carries no actionable information.
func avatarUpload(entry catalog.Entry) (*gitlab.GraphQLUpload, error) {
	if !entry.HasAvatar() {
		//nolint:nilnil // a nil upload is the "no avatar to attach" case, which the API reads as "leave the achievement's avatar alone"
		return nil, nil
	}

	file, err := entry.Avatar()
	if err != nil {
		return nil, fmt.Errorf("failed to open avatar for achievement %q: %w", entry.Name, err)
	}
	defer func() { _ = file.Close() }()

	upload, err := gitlab.NewGraphQLUpload(file, path.Base(entry.AvatarPath), "image/png")
	if err != nil {
		return nil, fmt.Errorf("failed to read avatar for achievement %q: %w", entry.Name, err)
	}

	return upload, nil
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

// detectAchievementDrift compares GitLab's own current record of an
// achievement (live) and the local row mirroring what was last pushed for
// it (existing) against the catalog entry, and reports what has to go back
// to GitLab.
//
// live is ground truth for Name and Description. Comparing against it
// rather than only against the local row is what catches an achievement
// hand-edited on GitLab while the row still matches the catalog, and what
// lets an adopted achievement be pulled in line with the entry that
// claimed it.
//
// Avatar drift is inferred from two signals, because GitLab exposes an
// achievement's AvatarURL but not its bytes, so live image content cannot
// be diffed against the catalog's asset:
//
//   - the catalog names an asset other than the one the row records having
//     pushed, which is how a swapped or renamed icon is noticed;
//   - the catalog expects an avatar and the live record carries none, which
//     is how one cleared on GitLab, or an adopted achievement that never had
//     one, is noticed.
//
// An achievement GitLab reports an avatar for that the catalog no longer
// expects is left alone: there is no supported way to clear an avatar
// through this API, so the row is simply brought back in line with the
// catalog (see reconcileAchievement) and no request is made.
func detectAchievementDrift(live *gitlab.Achievement, existing *db.AchievementDefinition, entry catalog.Entry) achievementDrift {
	liveDescription := ""
	if live.Description != nil {
		liveDescription = *live.Description
	}

	return achievementDrift{
		name:        live.Name != entry.Name,
		description: liveDescription != entry.Description,
		avatar:      entry.AvatarPath != "" && (existing.AvatarPath != entry.AvatarPath || live.AvatarURL == nil),
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
		avatar, err := avatarUpload(entry)
		if err != nil {
			return err
		}

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
//
// The row is left untouched only when it already mirrors the catalog and
// GitLab needs nothing. A row that has fallen behind the catalog in a way
// GitLab cannot be told about - an avatar dropped from an entry, which no
// API call can clear - is still saved, so the next pass does not rediscover
// the same difference forever.
func reconcileAchievement(ctx context.Context, write achievementWriter, conn *gorm.DB, live *gitlab.Achievement, existing *db.AchievementDefinition, entry catalog.Entry) (bool, error) {
	drift := detectAchievementDrift(live, existing, entry)

	mirrorsEntry := existing.Name == entry.Name &&
		existing.Description == entry.Description &&
		existing.AvatarPath == entry.AvatarPath &&
		existing.Threshold == entry.Threshold &&
		existing.ExpReward == entry.Exp

	if !drift.any() && mirrorsEntry {
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
