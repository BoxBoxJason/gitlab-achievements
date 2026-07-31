package bootstrap

import (
	"context"
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// AwardsReport summarizes what ReconcileAwards did.
type AwardsReport struct {
	// Confirmed counts awards GitLab now holds for their recipient.
	Confirmed int
	// Failed counts awards GitLab rejected, left for the next pass.
	Failed int
	// Superseded counts tiers withdrawn from GitLab, or never pushed to
	// it, because a higher tier of the same criteria took their place.
	Superseded int
	// Adopted counts awards this app had already delivered but held no
	// GitLab ID for, matched back to GitLab's own record rather than
	// awarded a second time.
	Adopted int
}

// ReconcileAwards pushes every locally recorded award GitLab doesn't hold
// yet, and withdraws the ones a higher tier has superseded.
//
// Only the top tier a user has reached in a criteria is ever on GitLab.
// The catalog stacks eleven tiers per criteria and the engine records every
// tier a user reaches, so pushing all of them would put a run of
// near-identical badges in front of the recipient, one notification email
// each (GitLab emails on every award, and nothing about awarding is
// batched). The lower tiers stay recorded here, and keep paying their EXP:
// what changes is only which single tier GitLab is asked to hold.
//
// Delivery is per user rather than per award because both halves of the
// decision are per user: which tier is currently the top one, and what
// GitLab already holds for them. Awarding is not idempotent on GitLab's
// side, so retrying an award blind is what creates duplicates; the pass
// reads GitLab's own record for a user once and matches against it instead.
//
// A GitLab call that fails is recorded and left for the next pass rather
// than aborting the run, consistent with the rest of this package's
// reconciliation: one user's rejected mutation shouldn't stop everyone
// else's awards from being delivered.
//
// Nothing here is a one-way door. Every pass decides what GitLab should
// hold from the awards a user has now, rather than from what an earlier
// pass concluded, so a tier withdrawn under one catalog and promoted back
// under the next is delivered again rather than left behind.
func ReconcileAwards(ctx context.Context, write achievementWriter, conn *gorm.DB) (AwardsReport, error) {
	var report AwardsReport

	userIDs, err := usersWithAwards(conn)
	if err != nil {
		return report, err
	}

	for _, userID := range userIDs {
		err = reconcileUserAwards(ctx, write, conn, userID, &report)
		if err != nil {
			return report, err
		}
	}

	return report, nil
}

// usersWithAwards lists everyone holding at least one award, which is
// everyone this pass reconciles.
//
// Narrowing this to the users with obviously outstanding work (a pending
// award, a failed one, a delivered one missing its ID) would be cheaper and
// would cover every case that arises from ordinary activity, since a tier
// is always pending before it can supersede anything. It would also make
// every other state terminal by assumption, including ones a catalog retune
// can produce: dropping a criteria's top tiers renumbers the stack in place
// (see catalog.Template.Expand), which can leave a tier this app already
// superseded as the highest one a user holds. Deciding fresh, for everyone,
// is what makes that heal on the next pass instead of never.
//
// It stays cheap because the decision is cheap: both halves of it return
// without touching GitLab when what GitLab holds already matches, so a
// sweep over an instance where nothing has been earned costs one indexed
// query per user and no API calls at all.
func usersWithAwards(conn *gorm.DB) ([]int64, error) {
	var userIDs []int64

	err := conn.Model(&db.Award{}).
		Distinct().
		Pluck("user_id", &userIDs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load users holding awards: %w", err)
	}

	return userIDs, nil
}

// reconcileUserAwards brings what GitLab holds for one user in line with
// what they have earned: their top tier per criteria and nothing else.
func reconcileUserAwards(ctx context.Context, write achievementWriter, conn *gorm.DB, userID int64, report *AwardsReport) error {
	var awards []db.Award

	err := conn.
		Preload("User").
		Preload("AchievementDefinition").
		Where("user_id = ?", userID).
		Find(&awards).Error
	if err != nil {
		return fmt.Errorf("failed to load awards for user %d: %w", userID, err)
	}

	if len(awards) == 0 {
		return nil
	}

	top := topTiers(awards)
	held := &heldAwards{write: write, username: awards[0].User.Username}

	for index := range awards {
		award := &awards[index]

		definition := award.AchievementDefinition
		if definition.Tier == top[definition.CriteriaKey] {
			err = deliverAward(ctx, conn, held, award, report)
		} else {
			err = supersedeAward(ctx, conn, held, award, report)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// topTiers reports the highest tier a user has earned in each criteria.
//
// Every award counts towards it, whatever its delivery status: the tier a
// user has reached is what the engine recorded, not what GitLab has been
// told about, so a run interrupted halfway through delivering a stack picks
// the same winner on its next attempt as it would have on its first.
func topTiers(awards []db.Award) map[string]int64 {
	top := make(map[string]int64, len(awards))

	for _, award := range awards {
		definition := award.AchievementDefinition
		if definition.Tier > top[definition.CriteriaKey] {
			top[definition.CriteriaKey] = definition.Tier
		}
	}

	return top
}

// deliverAward makes sure GitLab holds award, and that this app knows the
// ID GitLab holds it under.
//
// An award already delivered and identified is left alone. One delivered
// without its ID recorded is matched back to GitLab's own record rather
// than awarded again, because awarding twice means two awards and two
// emails, not one idempotent write.
func deliverAward(ctx context.Context, conn *gorm.DB, held *heldAwards, award *db.Award, report *AwardsReport) error {
	if award.Status == db.AwardStatusAccepted && award.GitLabUserAchievementID != 0 {
		return nil
	}

	existing, err := held.find(ctx, award.AchievementDefinition.GitLabAchievementID)
	if err != nil {
		return recordAwardFailure(conn, award, report)
	}

	if existing != nil {
		report.Adopted++

		return recordAwardDelivered(conn, award, existing, report)
	}

	awarded, awardErr := held.award(ctx, award)
	if awardErr != nil {
		return recordAwardFailure(conn, award, report)
	}

	return recordAwardDelivered(conn, award, awarded, report)
}

// supersedeAward takes a tier GitLab holds back off its recipient, now that
// they hold a higher one, and records that this app means it to be off.
//
// Revoking is what GitLab offers this app here: it is the only mutation on
// an existing award that the awarding token is allowed to make, it sends
// the recipient nothing, and it takes the badge off the profile whether or
// not they had accepted it. The local row survives it, so the tier stays
// earned and keeps paying its EXP.
//
// A tier that never reached GitLab is recorded without a call at all, which
// is the case that spares a first backfill most of its work: a user with
// years of history reaches most of a stack in one pass and only its top
// tier is ever awarded.
//
// A tier already superseded is not superseded again. That is a shortcut
// past work that is done, not a refusal to look: should the tier become the
// top one a user holds, the pass routes it to deliverAward instead and it
// goes back to GitLab under a fresh ID, since a revoked award can't be
// un-revoked.
func supersedeAward(ctx context.Context, conn *gorm.DB, held *heldAwards, award *db.Award, report *AwardsReport) error {
	if award.Status == db.AwardStatusSuperseded {
		return nil
	}

	if award.GitLabUserAchievementID != 0 {
		_, err := held.revoke(ctx, award.GitLabUserAchievementID)
		if err != nil {
			return recordAwardFailure(conn, award, report)
		}
	}

	err := conn.Model(award).Updates(map[string]any{
		"status":           db.AwardStatusSuperseded,
		"shown_on_profile": false,
	}).Error
	if err != nil {
		return fmt.Errorf("failed to persist superseded award %d: %w", award.ID, err)
	}

	report.Superseded++

	return nil
}

// recordAwardDelivered persists what GitLab answered with: the ID the award
// now has there, and whether its recipient has accepted it onto their
// profile.
//
// Acceptance is read back rather than assumed. A freshly awarded
// achievement is always hidden, but an award adopted from GitLab's record
// may be one the recipient accepted long ago, and this column is the only
// place that ever knows.
func recordAwardDelivered(conn *gorm.DB, award *db.Award, delivered *gitlab.UserAchievement, report *AwardsReport) error {
	err := conn.Model(award).Updates(map[string]any{
		"status":                      db.AwardStatusAccepted,
		"git_lab_user_achievement_id": delivered.ID,
		"shown_on_profile":            delivered.ShowOnProfile,
	}).Error
	if err != nil {
		return fmt.Errorf("failed to persist delivered award %d: %w", award.ID, err)
	}

	report.Confirmed++

	return nil
}

// recordAwardFailure marks an award GitLab wouldn't take, leaving it for the
// next reconciliation pass to retry. Only a local database error is returned
// to the caller: a rejected mutation is this pass's outcome for one award,
// not a reason to abandon the rest.
func recordAwardFailure(conn *gorm.DB, award *db.Award, report *AwardsReport) error {
	err := conn.Model(award).Update("status", db.AwardStatusFailed).Error
	if err != nil {
		return fmt.Errorf("failed to persist award %d status: %w", award.ID, err)
	}

	report.Failed++

	return nil
}

// heldAwards is what GitLab already holds for one user, fetched at most
// once however many of their awards need looking up, and not at all when
// none do.
//
// It exists because awarding is not idempotent: achievementsAward creates a
// new award every time it is called, so an award this app delivered but
// crashed before recording would be delivered again on the next pass, and
// the recipient would get a second email for a badge they already have.
// Matching against GitLab's own record first is what makes retrying safe.
//
// A lookup failure is remembered, so a user whose achievements couldn't be
// listed doesn't have the same failing request made once per award they
// hold.
type heldAwards struct {
	write         achievementWriter
	err           error
	byAchievement map[int64]*gitlab.UserAchievement
	username      string
	loaded        bool
}

// find returns the award GitLab already holds for this user against
// achievementID, or nil when it holds none.
func (h *heldAwards) find(ctx context.Context, achievementID int64) (*gitlab.UserAchievement, error) {
	err := h.load(ctx)
	if err != nil {
		return nil, err
	}

	return h.byAchievement[achievementID], nil
}

// load fetches every achievement GitLab holds for the user, including the
// ones they haven't accepted onto their profile, which is most of them:
// hidden awards are only listed to a caller entitled to see them, and the
// write token is.
//
// Revoked awards are skipped. GitLab keeps them listed, but a revoked award
// can be neither revoked again nor un-revoked, so matching one would leave
// this app pointing at a record it can no longer act on; treating it as
// absent re-awards the tier instead, which is the recoverable outcome.
func (h *heldAwards) load(ctx context.Context) error {
	if h.loaded {
		return h.err
	}

	h.loaded = true
	h.byAchievement = make(map[int64]*gitlab.UserAchievement)

	includeHidden := true
	opt := &gitlab.ListUserAchievementsOptions{IncludeHidden: &includeHidden}

	for userAchievement, err := range h.write.ListUserAchievements(h.username, opt, gitlab.WithContext(ctx)) {
		if err != nil {
			h.err = fmt.Errorf("failed to list achievements held by %q: %w", h.username, err)

			return h.err
		}

		if userAchievement.RevokedAt != nil {
			continue
		}

		h.byAchievement[userAchievement.AchievementID] = userAchievement
	}

	return nil
}

// award pushes one award to GitLab and records it as held, so that a later
// award of the same achievement in the same pass matches it rather than
// awarding it twice.
func (h *heldAwards) award(ctx context.Context, award *db.Award) (*gitlab.UserAchievement, error) {
	achievementID := award.AchievementDefinition.GitLabAchievementID

	delivered, err := h.write.AwardAchievement(achievementID, award.User.GitLabUserID, nil, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to award achievement %d to %q: %w", achievementID, h.username, err)
	}

	if h.byAchievement != nil {
		h.byAchievement[achievementID] = delivered
	}

	return delivered, nil
}

// revoke withdraws one award on GitLab and forgets it, so nothing later in
// the pass matches an award that is no longer actionable.
func (h *heldAwards) revoke(ctx context.Context, userAchievementID int64) (*gitlab.UserAchievement, error) {
	revoked, err := h.write.RevokeAchievement(userAchievementID, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to revoke award %d held by %q: %w", userAchievementID, h.username, err)
	}

	if h.byAchievement != nil {
		delete(h.byAchievement, revoked.AchievementID)
	}

	return revoked, nil
}
