package db

import "time"

// User maps a GitLab user to the local records tracking their achievement
// progress and awards.
//
// ExpTotal is the EXP the user has earned across every tier they hold. It
// lives here because GitLab has nowhere to put it: an achievement is a flat
// object with a name, a description and an avatar, with no points or level
// field, so this column is the only record of the number anywhere.
//
// It is a cache of a derived value, not an independent one: it always
// equals the sum of ExpReward over the user's awards, and the engine
// recomputes it from those awards rather than accumulating it forward (see
// engine.RecomputeEXP for why).
type User struct {
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Username     string `gorm:"not null"`
	ID           int64  `gorm:"primarykey"`
	GitLabUserID int64  `gorm:"uniqueIndex;not null"`
	ExpTotal     int64  `gorm:"column:exp_total;not null;default:0"`
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
//
// Threshold and ExpReward are local-only: GitLab stores neither, so they
// are never pushed anywhere and drift in them is corrected by updating this
// row alone. ExpReward is what earning this tier is worth towards its
// holder's User.ExpTotal.
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
	ExpReward           int64 `gorm:"column:exp_reward;not null;default:0"`
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

// AwardStatus describes where an Award stands in the delivery lifecycle
// with GitLab's achievements API.
//
// This tracks what this app has pushed to GitLab, which is a different
// question from whether the recipient has accepted what was pushed. Only
// the recipient can accept an award, and only they can undo it; see
// Award.ShownOnProfile for that half.
type AwardStatus string

const (
	// AwardStatusPending means the award has been recorded locally but not
	// yet pushed to GitLab.
	AwardStatusPending AwardStatus = "pending"
	// AwardStatusAccepted means GitLab took the awarding mutation and
	// created the award. It says nothing about the recipient having
	// accepted it onto their profile.
	AwardStatusAccepted AwardStatus = "accepted"
	// AwardStatusFailed means GitLab rejected or failed to grant the award.
	AwardStatusFailed AwardStatus = "failed"
	// AwardStatusSuperseded means a higher tier of the same criteria is
	// what GitLab holds for this user now, so this tier is deliberately
	// not on GitLab: it was either never pushed, or pushed and since
	// revoked. The row stays, and keeps paying its EXP, because the user
	// still earned the tier.
	AwardStatusSuperseded AwardStatus = "superseded"
)

// Award records that a user has earned (or is being granted) a specific
// achievement definition.
//
// GitLabUserAchievementID is the ID GitLab assigned the award itself, as
// distinct from the achievement it was made from. Every mutation that acts
// on an award somebody already holds is keyed by that ID and by nothing
// else, so an award delivered without recording it can never be revoked
// again. It is zero until GitLab has answered with one, and it is what
// makes delivery safe to retry: awarding is not idempotent on GitLab's
// side, so an award pushed twice becomes two separate records, and two
// notification emails to the recipient.
//
// ShownOnProfile records whether the recipient has accepted the award onto
// their profile. GitLab awards land hidden and stay hidden until the person
// who received them opts in, which only they can do, so this is read back
// from GitLab rather than set by this app. It is the only signal here that
// an award was ever actually seen by anyone.
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
	GitLabUserAchievementID int64                 `gorm:"index;not null;default:0"`
	ShownOnProfile          bool                  `gorm:"not null;default:false"`
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

// Session is a browser's authenticated session with the read API, created
// by the OAuth callback once GitLab has issued an access token for the
// person behind the browser.
//
// ID is an opaque random value and is what the session cookie carries, so
// possession of it is possession of the session; it is a primary key rather
// than a sequential one for exactly that reason.
//
// AccessToken is the GitLab token the session stands for, and is stored so
// that every request can be re-verified against GitLab. That is a
// deliberate consequence of verifying per request rather than caching an
// identity: the token is the only thing GitLab will answer questions about.
// It lives at rest here at the same trust level as the two GitLab tokens
// this app is already configured with.
//
// Sessions expire on ExpiresAt, which tracks the token's own expiry where
// GitLab supplies one. Expired rows are pruned rather than left to
// accumulate; see the api package's session store.
type Session struct {
	CreatedAt   time.Time
	ExpiresAt   time.Time `gorm:"not null;index"`
	ID          string    `gorm:"primarykey;size:64"`
	AccessToken string    `gorm:"not null"`
}

// SyncState stores backfill cursors and reconciliation watermarks, keyed by
// an arbitrary caller-defined key (for example a criteria key or job name).
type SyncState struct {
	UpdatedAt time.Time
	Key       string `gorm:"uniqueIndex;not null"`
	Value     string `gorm:"not null"`
	ID        int64  `gorm:"primarykey"`
}
