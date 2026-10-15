package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

const (
	// maxLeaderboardOffset caps how deep a caller may page. It is far past
	// the size of any instance this app serves, and only exists so a
	// nonsense offset is refused rather than handed to the database.
	maxLeaderboardOffset = 1_000_000

	// PeriodAll ranks by EXP held, regardless of when it was earned.
	PeriodAll = "all"

	// The rolling periods a leaderboard can be ranked over.
	day   = 24 * time.Hour
	week  = 7 * day
	month = 30 * day
	year  = 365 * day
)

// ErrCriteriaUnknown reports a per-criteria ranking asked for a criteria
// this app defines no achievements for.
var ErrCriteriaUnknown = errors.New("no such criteria")

// periods are the rolling windows a leaderboard can be ranked over. They are
// rolling rather than calendar-aligned on purpose: a calendar week starts on
// a different day in different places, and this app has no single timezone
// it could pick for everyone looking at the board.
//
//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var periods = map[string]time.Duration{
	"day":   day,
	"week":  week,
	"month": month,
	"year":  year,
}

// page is one slice of a ranking.
type page struct {
	limit  int
	offset int
}

// window is the span a leaderboard ranks EXP earned over. A period of
// PeriodAll means all time, and since is then unused.
type window struct {
	since  time.Time
	period string
}

// CriteriaEntry is one row of a per-criteria ranking.
type CriteriaEntry struct {
	Username     string `json:"username"`
	AvatarURL    string `json:"avatar_url"`
	GitLabUserID int64  `json:"gitlab_user_id"`
	Count        int64  `json:"count"`
	// Tier is the highest tier the count reaches, 0 for none yet.
	Tier int64 `json:"tier"`
	Rank int   `json:"rank"`
}

// CriteriaLeaderboard ranks users by one criteria's counter.
type CriteriaLeaderboard struct {
	CriteriaKey string          `json:"criteria_key"`
	Entries     []CriteriaEntry `json:"entries"`
	Limit       int             `json:"limit"`
	Offset      int             `json:"offset"`
	Total       int64           `json:"total"`
}

// parsePage resolves the limit and offset query parameters.
//
// Like the limit, an out-of-range offset is refused rather than clamped:
// a caller asking for page 10,000 of a 50-user instance should be told
// they have run off the end by an empty page, not quietly handed page 1.
func parsePage(req *http.Request) (page, error) {
	limit, err := leaderboardLimit(req.URL.Query().Get("limit"))
	if err != nil {
		return page{}, err
	}

	raw := req.URL.Query().Get("offset")
	if raw == "" {
		return page{limit: limit}, nil
	}

	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 || offset > maxLeaderboardOffset {
		return page{}, fmt.Errorf("offset must be a number between 0 and %d", maxLeaderboardOffset)
	}

	return page{limit: limit, offset: offset}, nil
}

// parseWindow resolves the period and since query parameters, which are
// two ways of saying the same thing and so may not both be given.
func parseWindow(req *http.Request, now time.Time) (window, error) {
	period := req.URL.Query().Get("period")
	rawSince := req.URL.Query().Get("since")

	if period != "" && rawSince != "" {
		return window{}, errors.New("give either period or since, not both")
	}

	if rawSince != "" {
		since, err := time.Parse(time.RFC3339, rawSince)
		if err != nil {
			return window{}, errors.New("since must be an RFC 3339 timestamp, e.g. 2026-01-01T00:00:00Z")
		}

		return window{since: since.UTC(), period: "custom"}, nil
	}

	if period == "" || period == PeriodAll {
		return window{period: PeriodAll}, nil
	}

	span, ok := periods[period]
	if !ok {
		return window{}, errors.New("period must be one of all, day, week, month, year")
	}

	return window{since: now.Add(-span).UTC(), period: period}, nil
}

// leaderboard ranks users by the EXP they hold.
func (s *store) leaderboard(ctx context.Context, slice page) ([]Entry, int64, error) {
	txn := s.conn.WithContext(ctx)

	var total int64

	err := db.Participating(txn.Model(&db.User{})).Count(&total).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count leaderboard: %w", err)
	}

	var users []db.User

	// git_lab_user_id, not gitlab_user_id: the column name is gorm's
	// rendering of the GitLabUserID field, and SQLite is the only one of the
	// four supported DBMSs that would reject the wrong spelling loudly.
	err = db.Participating(txn).
		Order("exp_total DESC, git_lab_user_id ASC").
		Limit(slice.limit).
		Offset(slice.offset).
		Find(&users).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to load leaderboard: %w", err)
	}

	entries := make([]Entry, 0, len(users))

	for index, user := range users {
		entries = append(entries, Entry{
			Summary: summaryOf(&user),
			Rank:    slice.offset + index + 1,
		})
	}

	return entries, total, nil
}

// periodRow is one user's EXP earned inside a window.
type periodRow struct {
	Username     string
	AvatarURL    string
	GitLabUserID int64
	ExpTotal     int64
	Earned       int64
}

// periodLeaderboard ranks users by the EXP of the tiers they earned inside
// the window.
//
// A tier's award is dated by the activity that earned it, not by when this
// app got round to evaluating it, so tiers earned during the historical
// backfill land in the past where they belong rather than all in the week
// the app was installed.
//
// This one is a JOIN rather than the two plain model queries the rest of
// the store uses, because the ranking is an aggregate over awards and has
// to be sorted and paged by the database: loading every award in the
// window to sum it in memory would not stay cheap on a busy instance. The
// SQL is the portable subset all four supported DBMSs share.
func (s *store) periodLeaderboard(ctx context.Context, slice page, since time.Time) ([]Entry, int64, error) {
	base := func() *gorm.DB {
		return db.Participating(s.conn.WithContext(ctx).Table("awards").
			Joins("JOIN achievement_definitions ON achievement_definitions.id = awards.achievement_definition_id").
			Joins("JOIN users ON users.id = awards.user_id")).
			Where("awards.awarded_at >= ?", since)
	}

	var total int64

	err := base().Distinct("awards.user_id").Count(&total).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count period leaderboard: %w", err)
	}

	var rows []periodRow

	err = base().
		Select("users.username AS username, users.avatar_url AS avatar_url, users.git_lab_user_id AS git_lab_user_id, " +
			"users.exp_total AS exp_total, SUM(achievement_definitions.exp_reward) AS earned").
		Group("users.id, users.username, users.avatar_url, users.git_lab_user_id, users.exp_total").
		Order("earned DESC, users.git_lab_user_id ASC").
		Limit(slice.limit).
		Offset(slice.offset).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to load period leaderboard: %w", err)
	}

	entries := make([]Entry, 0, len(rows))

	for index, row := range rows {
		earned := row.Earned
		entries = append(entries, Entry{
			Summary: Summary{
				Username:     row.Username,
				AvatarURL:    row.AvatarURL,
				GitLabUserID: row.GitLabUserID,
				ExpTotal:     row.ExpTotal,
			},
			ExpEarned: &earned,
			Rank:      slice.offset + index + 1,
		})
	}

	return entries, total, nil
}

// criteriaLeaderboard ranks users by one criteria's counter, reporting the
// highest tier each count reaches.
func (s *store) criteriaLeaderboard(ctx context.Context, criteriaKey string, slice page) (*CriteriaLeaderboard, error) {
	txn := s.conn.WithContext(ctx)

	definitions, err := s.definitionsOf(txn, criteriaKey)
	if err != nil {
		return nil, err
	}

	base := func() *gorm.DB {
		return db.Participating(txn.Table("progress_counters").
			Joins("JOIN users ON users.id = progress_counters.user_id")).
			Where("progress_counters.criteria_key = ? AND progress_counters.count > 0", criteriaKey)
	}

	var total int64

	err = base().Count(&total).Error
	if err != nil {
		return nil, fmt.Errorf("failed to count %q ranking: %w", criteriaKey, err)
	}

	var rows []struct {
		Username     string
		AvatarURL    string
		GitLabUserID int64
		Count        int64
	}

	err = base().
		Select("users.username AS username, users.avatar_url AS avatar_url, " +
			"users.git_lab_user_id AS git_lab_user_id, progress_counters.count AS count").
		Order("progress_counters.count DESC, users.git_lab_user_id ASC").
		Limit(slice.limit).
		Offset(slice.offset).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load %q ranking: %w", criteriaKey, err)
	}

	entries := make([]CriteriaEntry, 0, len(rows))

	for index, row := range rows {
		entries = append(entries, CriteriaEntry{
			Username:     row.Username,
			AvatarURL:    row.AvatarURL,
			GitLabUserID: row.GitLabUserID,
			Count:        row.Count,
			Tier:         tierReached(definitions, row.Count),
			Rank:         slice.offset + index + 1,
		})
	}

	return &CriteriaLeaderboard{
		CriteriaKey: criteriaKey,
		Entries:     entries,
		Limit:       slice.limit,
		Offset:      slice.offset,
		Total:       total,
	}, nil
}

// definitionsOf loads a criteria's tiers by ascending threshold, reporting
// ErrCriteriaUnknown when it has none.
func (s *store) definitionsOf(txn *gorm.DB, criteriaKey string) ([]db.AchievementDefinition, error) {
	var definitions []db.AchievementDefinition

	err := txn.Where(&db.AchievementDefinition{CriteriaKey: criteriaKey}).Order("threshold ASC").Find(&definitions).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load definitions for %q: %w", criteriaKey, err)
	}

	if len(definitions) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrCriteriaUnknown, criteriaKey)
	}

	return definitions, nil
}

// tierReached returns the highest tier count reaches among definitions,
// which are sorted by threshold.
func tierReached(definitions []db.AchievementDefinition, count int64) int64 {
	var tier int64

	for _, definition := range definitions {
		if count < definition.Threshold {
			break
		}

		tier = max(tier, definition.Tier)
	}

	return tier
}

// rank returns user's position on the all-time leaderboard, ordered as
// leaderboard orders it.
func (s *store) rank(txn *gorm.DB, user *db.User) (int, error) {
	var ahead int64

	err := db.Participating(txn.Model(&db.User{})).
		Where("exp_total > ? OR (exp_total = ? AND git_lab_user_id < ?)", user.ExpTotal, user.ExpTotal, user.GitLabUserID).
		Count(&ahead).Error
	if err != nil {
		return 0, fmt.Errorf("failed to rank user %d: %w", user.GitLabUserID, err)
	}

	return int(ahead) + 1, nil
}

// summaryOf is a user's identity and EXP total.
func summaryOf(user *db.User) Summary {
	return Summary{
		Username:     user.Username,
		AvatarURL:    user.AvatarURL,
		GitLabUserID: user.GitLabUserID,
		ExpTotal:     user.ExpTotal,
	}
}
