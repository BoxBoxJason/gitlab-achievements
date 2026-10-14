package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// ErrUserUnknown reports that this app has never seen any activity from the
// requested user, which is a different answer from a user it knows who has
// earned nothing: the first is a 404, the second a 200 with a zero total.
var ErrUserUnknown = errors.New("no activity recorded for this user")

// Summary is what a user's EXP total alone looks like on the wire.
//
// It carries the identity alongside the number so that a caller who looked
// the user up by username learns their GitLab ID (and vice versa) without a
// second request, and so a response is self-describing once it has been
// piped somewhere else.
type Summary struct {
	Username     string `json:"username"`
	GitLabUserID int64  `json:"gitlab_user_id"`
	ExpTotal     int64  `json:"exp_total"`
}

// Counter is one criteria's running total for a user.
type Counter struct {
	CriteriaKey string `json:"criteria_key"`
	Count       int64  `json:"count"`
}

// EarnedTier is one achievement tier a user holds.
//
// Status and ShownOnProfile are both reported because they answer different
// questions and neither implies the other: Status is how far this app has
// got pushing the award to GitLab, while ShownOnProfile is whether the
// recipient has accepted it onto their profile, which only they can do.
type EarnedTier struct {
	AwardedAt      time.Time      `json:"awarded_at"`
	CriteriaKey    string         `json:"criteria_key"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Status         db.AwardStatus `json:"status"`
	Tier           int64          `json:"tier"`
	Threshold      int64          `json:"threshold"`
	ExpReward      int64          `json:"exp_reward"`
	ShownOnProfile bool           `json:"shown_on_profile"`
}

// Detail is a user's whole record: their EXP, the criteria counters behind
// it, and the tiers they have earned.
//
// Counters and Awards are always encoded, as [] rather than null when
// empty, so a caller can index into them without a nil check.
//
// The embedded Summary is first because that is where an embedded field
// belongs; it costs eight bytes of padding that fieldalignment would rather
// have back, which is not a trade worth making on a response struct built
// once per request.
type Detail struct { //nolint:govet // see above: field order is for readers, not padding
	Summary

	// Rank is the user's position on the all-time leaderboard.
	Rank     int          `json:"rank"`
	Counters []Counter    `json:"counters"`
	Awards   []EarnedTier `json:"awards"`
}

// Entry is one row of the leaderboard.
//
// ExpEarned is present only on a leaderboard ranked over a period, where it
// is what the ranking is by: the EXP of the tiers earned inside it.
// ExpTotal stays the all-time figure either way, so a row never means two
// different things depending on how it was asked for.
//
// Field order is for readers of the JSON, as with Detail.
type Entry struct { //nolint:govet // see above: field order is for readers, not padding
	Summary

	ExpEarned *int64 `json:"exp_earned,omitempty"`
	Rank      int    `json:"rank"`
}

// Leaderboard is one page of the instance ranked by EXP.
//
// Total is how many users the whole ranking has, so a caller can page
// through it. Since is the start of the period ranked over, absent for all
// time.
type Leaderboard struct {
	Since   *time.Time `json:"since,omitempty"`
	Period  string     `json:"period"`
	Entries []Entry    `json:"entries"`
	Limit   int        `json:"limit"`
	Offset  int        `json:"offset"`
	Total   int64      `json:"total"`
}

// store answers the API's questions from the local database alone.
//
// Nothing here calls GitLab: the whole point of serving these totals from
// this app is that they stay available when the instance being mirrored is
// down or rate-limiting, so every read below is against tables this app
// owns.
type store struct {
	conn *gorm.DB
}

// Summary returns ref's EXP total and identity.
func (s *store) Summary(ctx context.Context, ref string) (*Summary, error) {
	user, err := s.lookup(ctx, ref)
	if err != nil {
		return nil, err
	}

	summary := summaryOf(user)

	return &summary, nil
}

// Detail returns ref's whole record.
func (s *store) Detail(ctx context.Context, ref string) (*Detail, error) {
	user, err := s.lookup(ctx, ref)
	if err != nil {
		return nil, err
	}

	return s.detailOf(ctx, user)
}

// DetailByGitLabID returns the whole record of the user with that GitLab
// ID, and only that user. Unlike Detail it never falls back to reading the
// number as a username, which would hand one person's record to somebody
// whose ID happens to spell another's all-digit username.
func (s *store) DetailByGitLabID(ctx context.Context, gitlabUserID int64) (*Detail, error) {
	if gitlabUserID <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrUserUnknown, gitlabUserID)
	}

	user, err := s.first(s.conn.WithContext(ctx), &db.User{GitLabUserID: gitlabUserID})
	if err != nil {
		return nil, err
	}

	if user == nil {
		return nil, fmt.Errorf("%w: %d", ErrUserUnknown, gitlabUserID)
	}

	return s.detailOf(ctx, user)
}

// mergeAwards pairs each award with its definition, dropping any award
// whose definition has gone.
//
// A missing definition is not an error to report: definitions are deleted
// only when a criteria leaves the catalog, and an award left behind by one
// describes an achievement that no longer exists, which is nothing a caller
// can act on. Bootstrap's reconciliation is what cleans those up.
func mergeAwards(rows []db.Award, byID map[int64]db.AchievementDefinition) []EarnedTier {
	tiers := make([]EarnedTier, 0, len(rows))

	for _, row := range rows {
		definition, ok := byID[row.AchievementDefinitionID]
		if !ok {
			continue
		}

		tiers = append(tiers, EarnedTier{
			AwardedAt:      row.AwardedAt,
			CriteriaKey:    definition.CriteriaKey,
			Name:           definition.Name,
			Description:    definition.Description,
			Status:         row.Status,
			Tier:           definition.Tier,
			Threshold:      definition.Threshold,
			ExpReward:      definition.ExpReward,
			ShownOnProfile: row.ShownOnProfile,
		})
	}

	sortTiers(tiers)

	return tiers
}

// sortTiers orders a user's tiers by criteria, then by tier within a
// criteria, so a response reads as a progression rather than in whatever
// order the rows came back in, and so two calls for an unchanged user
// return byte-identical bodies.
func sortTiers(tiers []EarnedTier) {
	slices.SortFunc(tiers, func(a, b EarnedTier) int {
		return cmp.Or(
			cmp.Compare(a.CriteriaKey, b.CriteriaKey),
			cmp.Compare(a.Tier, b.Tier),
		)
	})
}

// Leaderboard returns one page of the instance ranked by EXP: by EXP held
// for all time, or by EXP earned inside the window.
//
// The all-time ranking reads users.exp_total directly, with no join and no
// scan of the awards table, because that column is maintained as the sum
// of what its holder's tiers are worth. Ties are broken by GitLab user ID
// so that repeated calls, and consecutive pages, agree on the order.
func (s *store) Leaderboard(ctx context.Context, slice page, span window) (*Leaderboard, error) {
	var (
		entries []Entry
		total   int64
		err     error
		since   *time.Time
	)

	if span.period == PeriodAll {
		entries, total, err = s.leaderboard(ctx, slice)
	} else {
		entries, total, err = s.periodLeaderboard(ctx, slice, span.since)
		since = &span.since
	}

	if err != nil {
		return nil, err
	}

	return &Leaderboard{
		Entries: entries,
		Limit:   slice.limit,
		Offset:  slice.offset,
		Total:   total,
		Period:  span.period,
		Since:   since,
	}, nil
}

// detailOf assembles a found user's whole record.
func (s *store) detailOf(ctx context.Context, user *db.User) (*Detail, error) {
	txn := s.conn.WithContext(ctx)

	counters, err := s.counters(txn, user.ID)
	if err != nil {
		return nil, err
	}

	awards, err := s.awards(txn, user.ID)
	if err != nil {
		return nil, err
	}

	rank, err := s.rank(txn, user)
	if err != nil {
		return nil, err
	}

	return &Detail{
		Summary:  summaryOf(user),
		Rank:     rank,
		Counters: counters,
		Awards:   awards,
	}, nil
}

// lookup resolves the {ref} path segment to a user, trying it as a GitLab
// user ID before falling back to a username.
//
// Callers arrive holding one or the other depending on where they came
// from, so both work on one path. An all-digit ref is ambiguous, because a
// GitLab username may legally be all digits, and it is read as an ID first:
// that is the form callers are far likelier to hold programmatically, and
// an all-numeric username still resolves on the fallback unless it collides
// with a real user ID.
//
// The username half reads users.username, which the engine keeps in step
// with GitLab renames on every event, so somebody who was renamed resolves
// under the name this app last saw rather than 404ing.
func (s *store) lookup(ctx context.Context, ref string) (*db.User, error) {
	txn := s.conn.WithContext(ctx)

	// Guarded on > 0 rather than on parsing alone: gorm's struct queries
	// skip zero-valued fields, so a ref of "0" would otherwise build a
	// WHERE-less query and match an arbitrary user.
	userID, parseErr := strconv.ParseInt(ref, 10, 64)
	if parseErr == nil && userID > 0 {
		user, lookupErr := s.first(txn, &db.User{GitLabUserID: userID})
		if lookupErr != nil || user != nil {
			return user, lookupErr
		}
	}

	user, err := s.first(txn, &db.User{Username: ref})
	if err != nil {
		return nil, err
	}

	if user == nil {
		return nil, fmt.Errorf("%w: %q", ErrUserUnknown, ref)
	}

	return user, nil
}

// first runs one user query, reporting a miss as (nil, nil) so that callers
// can distinguish "no such user" from "the query failed" without inspecting
// gorm's sentinel at every call site.
//
// An excluded user is a miss too. They asked not to be shown, or are a bot
// nobody wants ranked, and answering 404 rather than 403 keeps the API from
// confirming that somebody who opted out exists.
func (s *store) first(txn *gorm.DB, where *db.User) (*db.User, error) {
	var user db.User

	err := txn.Where(where).Scopes(db.Participating).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil //nolint:nilnil // a miss is not an error here, see the doc comment
	}

	if err != nil {
		return nil, fmt.Errorf("failed to look up user: %w", err)
	}

	return &user, nil
}

// counters loads a user's criteria totals, ordered by criteria key so that
// two calls for an unchanged user return byte-identical bodies.
func (s *store) counters(txn *gorm.DB, userID int64) ([]Counter, error) {
	var rows []db.ProgressCounter

	err := txn.Where(&db.ProgressCounter{UserID: userID}).Order("criteria_key ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load progress counters for user %d: %w", userID, err)
	}

	counters := make([]Counter, 0, len(rows))
	for _, row := range rows {
		counters = append(counters, Counter{CriteriaKey: row.CriteriaKey, Count: row.Count})
	}

	return counters, nil
}

// awards loads the tiers a user holds, together with what each tier is and
// is worth.
//
// The definitions are fetched as a second query and joined in memory rather
// than with a JOIN clause, for the reason engine.heldExp gives: both halves
// stay ordinary gorm model queries, so table names, quoting and placeholder
// syntax all come from the models and the dialect instead of from SQL
// written for one DBMS's spelling.
//
// Every award counts whatever its delivery status, matching how EXP is
// totalled: a tier is earned when the engine says it was, and a superseded
// one is still held and still paid for, it is merely not what GitLab
// displays.
func (s *store) awards(txn *gorm.DB, userID int64) ([]EarnedTier, error) {
	var rows []db.Award

	err := txn.Where(&db.Award{UserID: userID}).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load awards for user %d: %w", userID, err)
	}

	if len(rows) == 0 {
		return []EarnedTier{}, nil
	}

	definitionIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		definitionIDs = append(definitionIDs, row.AchievementDefinitionID)
	}

	var definitions []db.AchievementDefinition

	err = txn.Where("id IN (?)", definitionIDs).Find(&definitions).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load achievement definitions for user %d: %w", userID, err)
	}

	byID := make(map[int64]db.AchievementDefinition, len(definitions))
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}

	return mergeAwards(rows, byID), nil
}
