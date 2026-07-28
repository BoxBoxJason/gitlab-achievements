package gitlabclient

import (
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// CreateAchievement creates a new achievement in the given namespace.
func (c *WriteClient) CreateAchievement(namespaceID int64, opt *gitlab.CreateAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error) {
	a, _, err := c.raw.Achievements.CreateAchievement(namespaceID, opt, options...)

	return a, err
}

// AwardAchievement awards achievementID to userID.
func (c *WriteClient) AwardAchievement(achievementID, userID int64, opt *gitlab.AwardAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error) {
	ua, _, err := c.raw.Achievements.AwardAchievement(achievementID, userID, opt, options...)

	return ua, err
}

// RevokeAchievement revokes a previously awarded achievement.
func (c *WriteClient) RevokeAchievement(userAchievementID int64, options ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error) {
	ua, _, err := c.raw.Achievements.RevokeAchievement(userAchievementID, options...)

	return ua, err
}
