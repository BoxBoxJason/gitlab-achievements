package db

import "time"

// User maps a GitLab user to the local records tracking their achievement
// progress and awards.
type User struct {
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Username     string `gorm:"not null"`
	ID           int64  `gorm:"primarykey"`
	GitLabUserID int64  `gorm:"uniqueIndex;not null"`
}

// AchievementDefinition is a local mirror of a GitLab achievement, tracking
// which criteria and tier it corresponds to and the progress threshold a
// user's counter must reach to earn it.
//
// Name, Description, and AvatarPath mirror the last values pushed to
// GitLab. The Achievements GraphQL API exposes no way to list or look up
// existing achievements by name, so this row (keyed by CriteriaKey+Tier) is
// the source of truth bootstrap uses both to find the GitLab-side
// achievement ID on repeat startups and to detect catalog drift worth
// pushing via achievementsUpdate.
type AchievementDefinition struct {
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CriteriaKey         string `gorm:"not null;index:idx_criteria_tier,unique"`
	Name                string `gorm:"not null"`
	Description         string
	AvatarPath          string
	GitLabAchievementID int64 `gorm:"uniqueIndex;not null"`
	ID                  int64 `gorm:"primarykey"`
	Tier                int64 `gorm:"not null;index:idx_criteria_tier,unique"`
	Threshold           int64 `gorm:"not null"`
}

// ProgressCounter tracks a single user's running total for a given
// achievement criteria, independent of any specific tier's threshold.
type ProgressCounter struct {
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CriteriaKey string `gorm:"not null;index:idx_user_criteria,unique"`
	User        User   `gorm:"constraint:OnDelete:CASCADE"`
	ID          int64  `gorm:"primarykey"`
	UserID      int64  `gorm:"not null;index:idx_user_criteria,unique"`
	Count       int64  `gorm:"not null;default:0"`
}

// AwardStatus describes where an Award stands in the acceptance lifecycle
// with GitLab's achievements API.
type AwardStatus string

const (
	// AwardStatusPending means the award has been recorded locally but not
	// yet confirmed as accepted by GitLab.
	AwardStatusPending AwardStatus = "pending"
	// AwardStatusAccepted means GitLab confirmed the award was granted.
	AwardStatusAccepted AwardStatus = "accepted"
	// AwardStatusFailed means GitLab rejected or failed to grant the award.
	AwardStatusFailed AwardStatus = "failed"
)

// Award records that a user has earned (or is being granted) a specific
// achievement definition.
type Award struct {
	AwardedAt               time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
	Status                  AwardStatus           `gorm:"not null;default:pending"`
	User                    User                  `gorm:"constraint:OnDelete:CASCADE"`
	AchievementDefinition   AchievementDefinition `gorm:"constraint:OnDelete:CASCADE"`
	ID                      int64                 `gorm:"primarykey"`
	UserID                  int64                 `gorm:"not null;index:idx_user_achievement,unique"`
	AchievementDefinitionID int64                 `gorm:"not null;index:idx_user_achievement,unique"`
}

// ProcessedEvent records that a GitLab event has already been ingested, so
// that redelivered or replayed events can be discarded idempotently.
type ProcessedEvent struct {
	ProcessedAt time.Time `gorm:"not null"`
	CreatedAt   time.Time
	EventID     string `gorm:"uniqueIndex;not null"`
	EventType   string `gorm:"not null"`
	ID          int64  `gorm:"primarykey"`
}

// SyncState stores backfill cursors and reconciliation watermarks, keyed by
// an arbitrary caller-defined key (for example a criteria key or job name).
type SyncState struct {
	UpdatedAt time.Time
	Key       string `gorm:"uniqueIndex;not null"`
	Value     string `gorm:"not null"`
	ID        int64  `gorm:"primarykey"`
}
