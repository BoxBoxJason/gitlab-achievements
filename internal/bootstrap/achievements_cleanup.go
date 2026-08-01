package bootstrap

import (
	"context"
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// achievementRemover is the subset of gitlabclient.WriteClient achievement
// removal needs.
type achievementRemover interface {
	DeleteAchievement(achievementID int64, options ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error)
	ListAchievements(fullPath string, opt *gitlab.ListAchievementsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Achievement, error]
}

// AchievementCleanupReport summarizes what an achievement removal pass did.
type AchievementCleanupReport struct {
	// Deleted counts achievements removed from the namespace.
	Deleted int
	// AlreadyGone counts achievements this app had recorded but that GitLab
	// no longer has, because somebody deleted them by hand.
	AlreadyGone int
	// Skipped counts achievements left in place because this app's token
	// may not remove them. Their rows are kept, so a later run with a
	// better-privileged token still knows what to clean up.
	Skipped int
	// Swept counts the achievements the namespace sweep enumerated, and is
	// zero unless CleanupOptions.Sweep was set.
	Swept int
}

// RemoveAchievements deletes the achievement definitions this app created,
// which is the other half of removing it from a GitLab instance.
//
// This is more destructive than removing the webhooks, and irreversibly so:
// GitLab deletes every award of an achievement along with the achievement
// itself, so the badges disappear from the profiles of everyone who earned
// one, and re-running the app creates new achievements that nobody holds.
// The EXP totals in this app's own database survive, since they are derived
// from recorded activity rather than from what GitLab holds.
//
// As with the hooks, the recorded rows are the source of truth: every
// achievement this app created has a db.AchievementDefinition row carrying
// the GitLab ID. Rows are dropped as their achievements go, so an
// interrupted pass leaves behind exactly what it had not reached yet.
func RemoveAchievements(
	ctx context.Context,
	write achievementRemover,
	conn *gorm.DB,
	cfg *config.Config,
	opts CleanupOptions,
) (AchievementCleanupReport, error) {
	cleanup := &achievementCleanup{
		write:   write,
		conn:    conn,
		limiter: sweepLimiter(cfg.HookRate),
		dryRun:  opts.DryRun,
	}

	err := cleanup.removeRecorded(ctx)
	if err != nil {
		return AchievementCleanupReport{}, err
	}

	if opts.Sweep {
		err = cleanup.sweepNamespace(ctx, cfg.AchievementsNamespace)
		if err != nil {
			return AchievementCleanupReport{}, err
		}
	}

	return cleanup.report, nil
}

// achievementCleanup carries the state one removal pass threads through its
// work.
type achievementCleanup struct {
	write   achievementRemover
	conn    *gorm.DB
	limiter *rate.Limiter
	report  AchievementCleanupReport
	dryRun  bool
}

// removeRecorded deletes every achievement this app recorded creating.
func (c *achievementCleanup) removeRecorded(ctx context.Context) error {
	var batch []db.AchievementDefinition

	err := c.conn.WithContext(ctx).
		Model(&db.AchievementDefinition{}).
		FindInBatches(&batch, hookPageSize, func(_ *gorm.DB, _ int) error {
			for _, recorded := range batch {
				paceErr := paceSweep(ctx, c.limiter)
				if paceErr != nil {
					return paceErr
				}

				removeErr := c.remove(ctx, recorded.GitLabAchievementID, recorded.Name)
				if removeErr != nil {
					return removeErr
				}
			}

			return nil
		}).Error
	if err != nil {
		return fmt.Errorf("failed to remove the recorded achievements: %w", err)
	}

	return nil
}

// sweepNamespace deletes achievements in the namespace that removeRecorded
// had no record of, for the same case --sweep exists for on the hooks: a
// database lost, restored from an older backup, or replaced while the
// namespace kept what an earlier run had created.
//
// Only achievements whose name matches a catalog entry are touched. The
// namespace is an ordinary GitLab group that may hold achievements somebody
// created by hand, and a sweep that deleted those would be removing things
// this app never made. It is the same test the hook sweep applies with its
// URL: recognizably ours, or left alone.
func (c *achievementCleanup) sweepNamespace(ctx context.Context, namespaceFullPath string) error {
	ours := make(map[string]struct{})
	for _, entry := range catalog.V1() {
		ours[entry.Name] = struct{}{}
	}

	for achievement, err := range c.write.ListAchievements(namespaceFullPath, nil, gitlab.WithContext(ctx)) {
		if err != nil {
			return fmt.Errorf("failed to list the achievements in %q: %w", namespaceFullPath, err)
		}

		c.report.Swept++

		if _, mine := ours[achievement.Name]; !mine {
			continue
		}

		err = paceSweep(ctx, c.limiter)
		if err != nil {
			return err
		}

		err = c.remove(ctx, achievement.ID, achievement.Name)
		if err != nil {
			return err
		}
	}

	return nil
}

// remove deletes one achievement from GitLab and forgets this app's record
// of it.
//
// An achievement GitLab no longer has counts as gone rather than as a
// failure: somebody having deleted it by hand is the outcome this is
// reaching for anyway. One this app's token may not touch is left in place
// with its row intact, so re-running with a better-privileged token still
// knows where it is.
func (c *achievementCleanup) remove(ctx context.Context, achievementID int64, name string) error {
	if c.dryRun {
		c.report.Deleted++

		zap.L().Info("would remove achievement",
			zap.String("achievement", name),
			zap.Int64("gitlab_achievement_id", achievementID),
		)

		return nil
	}

	_, err := c.write.DeleteAchievement(achievementID, gitlab.WithContext(ctx))

	switch {
	case err == nil:
		c.report.Deleted++
	case gitlabclient.IsNotFound(err):
		c.report.AlreadyGone++
	case gitlabclient.IsPermissionError(err):
		zap.L().Warn("leaving behind an achievement this app cannot remove",
			zap.String("achievement", name),
			zap.Int64("gitlab_achievement_id", achievementID),
			zap.Error(err),
		)

		c.report.Skipped++

		return nil
	default:
		return fmt.Errorf("failed to remove achievement %q (%d): %w", name, achievementID, err)
	}

	return c.forget(achievementID)
}

// forget drops the record of an achievement that is no longer on GitLab.
//
// The awards referencing it go with it, by the cascade db.Award declares:
// a tier whose achievement no longer exists cannot be delivered, adopted or
// revoked, so keeping the rows would only leave the next pass retrying
// against an ID GitLab has forgotten.
func (c *achievementCleanup) forget(achievementID int64) error {
	err := c.conn.
		Where("git_lab_achievement_id = ?", achievementID).
		Delete(&db.AchievementDefinition{}).Error
	if err != nil {
		return fmt.Errorf("failed to forget achievement %d: %w", achievementID, err)
	}

	return nil
}
