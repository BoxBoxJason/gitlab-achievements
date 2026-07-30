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

// ActivityDay records that a user was active on one calendar day, and
// whether that day's activity fell in a notable part of the clock.
//
// Criteria like "active on N separate days", "N days in a row", and the
// night-owl/early-bird ones can't be answered by a running total, because
// the same day must count once however many events land on it, and a
// streak depends on which days neighbor which. Recording the days
// themselves and deriving those counters from the set keeps the answer
// independent of the order events arrive in, which matters because the
// historical backfill walks project by project rather than in date order,
// and may resume mid-walk.
//
// Date is stored as a YYYY-MM-DD string rather than a date column: it
// behaves identically across all four supported DBMSs, sorts
// lexicographically in calendar order, and is compared as an exact value
// rather than an instant, which is what "the same day" means here.
type ActivityDay struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	Date      string `gorm:"not null;index:idx_user_date,unique"`
	User      User   `gorm:"constraint:OnDelete:CASCADE"`
	ID        int64  `gorm:"primarykey"`
	UserID    int64  `gorm:"not null;index:idx_user_date,unique"`
	// NightOwl records that some of the day's activity happened in the
	// small hours, EarlyBird that some of it happened before the working
	// day. Both are latched on: a day that was ever a night-owl day stays
	// one, whatever else the user did later that day.
	NightOwl  bool `gorm:"not null;default:false"`
	EarlyBird bool `gorm:"not null;default:false"`
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

// HookScope names the kind of GitLab object a RegisteredHook is attached to.
type HookScope string

const (
	// HookScopeGroup is a webhook registered on a top-level group, covering
	// every project in that group and its subgroups. Requires Premium.
	HookScopeGroup HookScope = "group"
	// HookScopeProject is a webhook registered on a single project, used on
	// instances where group webhooks are unavailable.
	HookScopeProject HookScope = "project"
)

// RegisteredHook records a webhook this app registered on GitLab, so the
// periodic reconciliation can re-check it with a direct lookup by ID
// instead of listing every hook on every group or project.
//
// TargetID is the group or project the hook is attached to, and is unique
// per scope: this app registers at most one hook per target. The row is
// written only once the hook exists on GitLab, so a row that is present but
// whose hook 404s means the hook was deleted out of band and should be
// recreated.
type RegisteredHook struct {
	CreatedAt time.Time
	UpdatedAt time.Time
	Scope     HookScope `gorm:"not null;index:idx_scope_target,unique"`
	ID        int64     `gorm:"primarykey"`
	TargetID  int64     `gorm:"not null;index:idx_scope_target,unique"`
	HookID    int64     `gorm:"not null"`
}

// SyncState stores backfill cursors and reconciliation watermarks, keyed by
// an arbitrary caller-defined key (for example a criteria key or job name).
type SyncState struct {
	UpdatedAt time.Time
	Key       string `gorm:"uniqueIndex;not null"`
	Value     string `gorm:"not null"`
	ID        int64  `gorm:"primarykey"`
}
