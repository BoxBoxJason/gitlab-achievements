package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ListAchievements iterates every achievement defined in the namespace at
// fullPath.
func (c *WriteClient) ListAchievements(fullPath string, opt *gitlab.ListAchievementsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Achievement, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.Achievement, *gitlab.Response, error) {
		return c.raw.Achievements.ListAchievements(fullPath, opt, withExtra(options, reqOpts...)...)
	})
}

// CreateAchievement creates a new achievement in the given namespace.
func (c *WriteClient) CreateAchievement(namespaceID int64, opt *gitlab.CreateAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error) {
	a, _, err := c.raw.Achievements.CreateAchievement(namespaceID, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to create achievement: %w", err)
	}

	return a, nil
}

// UpdateAchievement updates an existing achievement's name and/or description.
func (c *WriteClient) UpdateAchievement(achievementID int64, opt *gitlab.UpdateAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error) {
	a, _, err := c.raw.Achievements.UpdateAchievement(achievementID, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to update achievement: %w", err)
	}

	return a, nil
}

// DeleteAchievement removes an achievement definition from its namespace.
//
// GitLab takes every award of it down with it, so this is what uninstalling
// the app's achievements amounts to: the badges stop existing, on the
// namespace and on the profiles of everyone who held one.
func (c *WriteClient) DeleteAchievement(achievementID int64, options ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error) {
	a, _, err := c.raw.Achievements.DeleteAchievement(achievementID, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to delete achievement: %w", err)
	}

	return a, nil
}

// AwardAchievement awards achievementID to userID.
func (c *WriteClient) AwardAchievement(achievementID, userID int64, opt *gitlab.AwardAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error) {
	ua, _, err := c.raw.Achievements.AwardAchievement(achievementID, userID, opt, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to award achievement: %w", err)
	}

	return ua, nil
}

// RevokeAchievement revokes a previously awarded achievement.
func (c *WriteClient) RevokeAchievement(userAchievementID int64, options ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error) {
	ua, _, err := c.raw.Achievements.RevokeAchievement(userAchievementID, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to revoke achievement: %w", err)
	}

	return ua, nil
}

// The two mutations that decide what an awarded achievement looks like on
// its recipient's profile, userAchievementsUpdate (showOnProfile) and
// userAchievementPrioritiesUpdate (ordering), are deliberately not wrapped
// here. GitLab gates both behind being the recipient
// (Achievements::UserAchievementPolicy grants update_user_achievement only
// via update_owned_user_achievement, which requires user_is_recipient), and
// there is no admin override: an instance admin calling either on someone
// else's award is refused, and GraphQL ignores the Sudo header. Wrapping
// them would only offer this app's write token calls it can never make
// successfully. See docs/achievements-api-behavior.md.

// ListUserAchievements iterates every achievement awarded to username,
// including the ones hidden from their profile.
//
// Hidden awards are only returned to a caller allowed to see them (the
// holder themself, or a maintainer/owner of the namespace, or an instance
// admin), which the write token is. Awards land hidden and stay hidden
// until their recipient accepts them, so without IncludeHidden a freshly
// awarded achievement isn't listed at all.
func (c *WriteClient) ListUserAchievements(username string, opt *gitlab.ListUserAchievementsOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.UserAchievement, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.UserAchievement, *gitlab.Response, error) {
		return c.raw.Achievements.ListUserAchievements(username, opt, withExtra(options, reqOpts...)...)
	})
}
