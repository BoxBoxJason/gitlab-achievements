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
