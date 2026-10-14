package api

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"
	"time"

	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// seedDefinition creates one tier's definition and returns it.
func seedDefinition(t *testing.T, conn *gorm.DB, criteria string, tier, threshold, exp int64, avatar string) appdb.AchievementDefinition {
	t.Helper()

	definition := appdb.AchievementDefinition{
		CriteriaKey:         criteria,
		Name:                criteria + " " + string(rune('A'+tier)),
		Description:         "earned by doing things",
		AvatarPath:          avatar,
		GitLabAchievementID: tier*1000 + int64(len(criteria)) + threshold,
		Tier:                tier,
		Threshold:           threshold,
		ExpReward:           exp,
	}
	if err := conn.Create(&definition).Error; err != nil {
		t.Fatalf("failed to seed definition: %v", err)
	}

	return definition
}

// seedAwardAt awards definition to userID as earned at awardedAt.
func seedAwardAt(t *testing.T, conn *gorm.DB, userID int64, definition appdb.AchievementDefinition, awardedAt time.Time) {
	t.Helper()

	award := appdb.Award{
		AwardedAt:               awardedAt,
		Status:                  appdb.AwardStatusAccepted,
		UserID:                  userID,
		AchievementDefinitionID: definition.ID,
	}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}
}

func TestLeaderboard_Pages(t *testing.T) {
	conn := testConn(t)

	for index, exp := range []int64{500, 400, 300, 200, 100} {
		seedUser(t, conn, int64(index+1), "user"+string(rune('a'+index)), exp)
	}

	board := decode[Leaderboard](t, get(t, openAPI(conn), "/api/v1/leaderboard?limit=2&offset=2"))

	if board.Total != 5 || board.Offset != 2 || board.Limit != 2 || board.Period != PeriodAll || board.Since != nil {
		t.Fatalf("unexpected page metadata: %+v", board)
	}

	if len(board.Entries) != 2 || board.Entries[0].Rank != 3 || board.Entries[0].ExpTotal != 300 || board.Entries[1].Rank != 4 {
		t.Fatalf("expected ranks 3 and 4 on the second page, got %+v", board.Entries)
	}

	if board.Entries[0].ExpEarned != nil {
		t.Error("expected no exp_earned on the all-time leaderboard")
	}

	past := decode[Leaderboard](t, get(t, openAPI(conn), "/api/v1/leaderboard?offset=50"))
	if len(past.Entries) != 0 || past.Total != 5 {
		t.Fatalf("expected an empty page past the end, got %+v", past)
	}
}

func TestLeaderboard_RejectsBadPagingAndWindows(t *testing.T) {
	conn := testConn(t)

	for _, query := range []string{
		"offset=-1",
		"offset=abc",
		"period=fortnight",
		"since=yesterday",
		"period=week&since=2026-01-01T00:00:00Z",
	} {
		rec := get(t, openAPI(conn), "/api/v1/leaderboard?"+query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for %q, got %d", query, rec.Code)
		}
	}
}

func TestLeaderboard_RanksAPeriodByEXPEarnedInIt(t *testing.T) {
	conn := testConn(t)
	now := time.Now().UTC()

	veteran := seedUser(t, conn, 1, "veteran", 5000)
	newcomer := seedUser(t, conn, 2, "newcomer", 150)
	seedUser(t, conn, 3, "idle", 0)

	big := seedDefinition(t, conn, "commits", 5, 500, 5000, "")
	small := seedDefinition(t, conn, "commits", 1, 1, 50, "")
	medium := seedDefinition(t, conn, "pushes", 1, 1, 100, "")

	seedAwardAt(t, conn, veteran, big, now.Add(-90*24*time.Hour))
	seedAwardAt(t, conn, newcomer, small, now.Add(-2*24*time.Hour))
	seedAwardAt(t, conn, newcomer, medium, now.Add(-1*time.Hour))

	board := decode[Leaderboard](t, get(t, openAPI(conn), "/api/v1/leaderboard?period=week"))

	if board.Period != "week" || board.Since == nil || board.Total != 1 {
		t.Fatalf("unexpected metadata: %+v", board)
	}

	if len(board.Entries) != 1 || board.Entries[0].Username != "newcomer" {
		t.Fatalf("expected only the newcomer to have earned anything this week, got %+v", board.Entries)
	}

	entry := board.Entries[0]
	if entry.ExpEarned == nil || *entry.ExpEarned != 150 || entry.ExpTotal != 150 || entry.Rank != 1 {
		t.Fatalf("expected 150 EXP earned this week, got %+v", entry)
	}

	year := decode[Leaderboard](t, get(t, openAPI(conn), "/api/v1/leaderboard?period=year"))
	if len(year.Entries) != 2 || year.Entries[0].Username != "veteran" || *year.Entries[0].ExpEarned != 5000 {
		t.Fatalf("expected the veteran first over the year, got %+v", year.Entries)
	}

	since := now.Add(-3 * time.Hour).Format(time.RFC3339)

	custom := decode[Leaderboard](t, get(t, openAPI(conn), "/api/v1/leaderboard?since="+since))
	if custom.Period != "custom" || len(custom.Entries) != 1 || *custom.Entries[0].ExpEarned != 100 {
		t.Fatalf("expected only the push tier inside the last three hours, got %+v", custom)
	}
}

func TestLeaderboard_PeriodLeavesOutExcludedUsers(t *testing.T) {
	conn := testConn(t)

	bot := seedUser(t, conn, 1, "project_1_bot", 9000)
	definition := seedDefinition(t, conn, "commits", 1, 1, 9000, "")
	seedAwardAt(t, conn, bot, definition, time.Now().UTC())

	if err := conn.Model(&appdb.User{}).Where("id = ?", bot).Update("excluded_reason", "bot").Error; err != nil {
		t.Fatalf("failed to exclude: %v", err)
	}

	board := decode[Leaderboard](t, get(t, openAPI(conn), "/api/v1/leaderboard?period=day"))
	if len(board.Entries) != 0 || board.Total != 0 {
		t.Fatalf("expected an excluded user to be left out of period rankings, got %+v", board)
	}
}

func TestCriteriaLeaderboard_RanksByCountWithTier(t *testing.T) {
	conn := testConn(t)

	seedDefinition(t, conn, "commits", 1, 1, 10, "")
	seedDefinition(t, conn, "commits", 2, 10, 20, "")
	seedDefinition(t, conn, "commits", 3, 100, 40, "")

	alice := seedUser(t, conn, 1, "alice", 0)
	bob := seedUser(t, conn, 2, "bob", 0)
	carol := seedUser(t, conn, 3, "carol", 0)

	seedCounter(t, conn, alice, "commits", 42)
	seedCounter(t, conn, bob, "commits", 120)
	seedCounter(t, conn, carol, "commits", 0)
	seedCounter(t, conn, carol, "pushes", 999)

	rec := get(t, openAPI(conn), "/api/v1/leaderboard/criteria/commits")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	board := decode[CriteriaLeaderboard](t, rec)
	if board.Total != 2 || len(board.Entries) != 2 {
		t.Fatalf("expected the two users with commits, got %+v", board)
	}

	if first := board.Entries[0]; first.Username != "bob" || first.Count != 120 || first.Tier != 3 || first.Rank != 1 {
		t.Errorf("unexpected first entry: %+v", first)
	}

	if second := board.Entries[1]; second.Username != "alice" || second.Tier != 2 || second.Rank != 2 {
		t.Errorf("unexpected second entry: %+v", second)
	}
}

func TestCriteriaLeaderboard_UnknownCriteriaAndPeriods(t *testing.T) {
	conn := testConn(t)
	seedDefinition(t, conn, "commits", 1, 1, 10, "")

	if rec := get(t, openAPI(conn), "/api/v1/leaderboard/criteria/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown criteria, got %d", rec.Code)
	}

	if rec := get(t, openAPI(conn), "/api/v1/leaderboard/criteria/commits?period=week"); rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a period on a per-criteria ranking, got %d", rec.Code)
	}
}

func TestHandleUser_ReportsRank(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 1, "alice", 900)
	seedUser(t, conn, 2, "bob", 500)
	seedUser(t, conn, 3, "carol", 500)

	detail := decode[Detail](t, get(t, openAPI(conn), "/api/v1/users/carol"))
	if detail.Rank != 3 {
		t.Fatalf("expected carol third, behind bob on the ID tie-break, got %d", detail.Rank)
	}

	board := decode[Leaderboard](t, get(t, openAPI(conn), "/api/v1/leaderboard"))
	if board.Entries[2].Username != "carol" || board.Entries[2].Rank != detail.Rank {
		t.Fatalf("expected the record's rank to agree with the leaderboard, got %+v", board.Entries)
	}
}

func TestAchievements_ServesTheCatalogGroupedInCatalogOrder(t *testing.T) {
	conn := testConn(t)

	seedDefinition(t, conn, "pushes", 2, 10, 20, "assets/push.png")
	seedDefinition(t, conn, "commits", 1, 1, 10, "assets/commit.png")
	seedDefinition(t, conn, "pushes", 1, 1, 10, "assets/push.png")
	seedDefinition(t, conn, "retired_criteria", 1, 1, 5, "")

	got := decode[Catalog](t, get(t, openAPI(conn), "/api/v1/achievements"))

	if len(got.Criteria) != 3 {
		t.Fatalf("expected three criteria, got %+v", got.Criteria)
	}

	commits, pushes, retired := got.Criteria[0], got.Criteria[1], got.Criteria[2]

	if commits.CriteriaKey != "commits" || commits.Category != "git" || commits.Title != "Committer" ||
		commits.IconURL != "/api/v1/achievements/commits/icon" {
		t.Errorf("unexpected commits row: %+v", commits)
	}

	if pushes.CriteriaKey != "pushes" || len(pushes.Tiers) != 2 || pushes.Tiers[0].Tier != 1 || pushes.Tiers[1].Threshold != 10 {
		t.Errorf("expected pushes with its tiers in order, got %+v", pushes)
	}

	if retired.CriteriaKey != "retired_criteria" || retired.Category != "" || retired.IconURL != "" {
		t.Errorf("expected a criteria gone from the catalog to be listed last, uncategorized, got %+v", retired)
	}
}

func TestAchievementIcon_ServesTheEmbeddedImage(t *testing.T) {
	conn := testConn(t)

	rec := get(t, openAPI(conn), "/api/v1/achievements/commits/icon")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") == "" || rec.Header().Get("ETag") == "" {
		t.Errorf("unexpected headers: %v", rec.Header())
	}

	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG")) {
		t.Error("expected a PNG body")
	}

	// The binary knows nothing of a criteria gone from the catalog, even
	// one the database still holds an image path for.
	seedDefinition(t, conn, "retired_criteria", 1, 1, 5, "assets/commit.png")

	for _, path := range []string{"/api/v1/achievements/retired_criteria/icon", "/api/v1/achievements/nope/icon"} {
		if rec := get(t, openAPI(conn), path); rec.Code != http.StatusNotFound {
			t.Errorf("expected 404 for %s, got %d", path, rec.Code)
		}
	}
}

func TestAchievementIcon_RevalidatesWithTheETag(t *testing.T) {
	api := openAPI(testConn(t))

	etag := get(t, api, "/api/v1/achievements/commits/icon").Header().Get("ETag")

	rec := get(t, api, "/api/v1/achievements/commits/icon", func(req *http.Request) {
		req.Header.Set("If-None-Match", etag)
	})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("expected a bodiless 304 for a matching ETag, got %d with %d bytes", rec.Code, rec.Body.Len())
	}
}

// An <img> tag cannot send a bearer token, so the icon has to load without
// one even where the rest of the API needs it.
func TestAchievementIcon_ServedWithoutACredential(t *testing.T) {
	conn := testConn(t)
	verifier := &stubVerifier{identity: &gitlabclient.Identity{ID: 1, Username: "caller"}}
	api := authenticatedAPI(t, conn, verifier)

	if rec := get(t, api, "/api/v1/achievements/commits/icon"); rec.Code != http.StatusOK {
		t.Errorf("expected the icon without a credential, got %d", rec.Code)
	}

	if verifier.calls != 0 {
		t.Errorf("expected no GitLab verification for an icon, got %d calls", verifier.calls)
	}

	if rec := get(t, api, "/api/v1/achievements"); rec.Code != http.StatusUnauthorized {
		t.Errorf("expected the catalog itself to stay behind authentication, got %d", rec.Code)
	}
}

// The documented way to page one fixed window is to pass back the since a
// period's first page reported, so it has to come back as the same instant.
func TestLeaderboard_PeriodSinceRoundTrips(t *testing.T) {
	api := openAPI(testConn(t))

	first := decode[Leaderboard](t, get(t, api, "/api/v1/leaderboard?period=week"))
	if first.Since == nil {
		t.Fatal("expected a period page to report since")
	}

	next := decode[Leaderboard](t, get(t, api, "/api/v1/leaderboard?since="+url.QueryEscape(first.Since.Format(time.RFC3339Nano))))
	if next.Since == nil || !next.Since.Equal(*first.Since) || next.Period != "custom" {
		t.Errorf("expected the same window back as custom, got %+v against %s", next, first.Since)
	}
}
