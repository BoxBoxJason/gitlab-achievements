package api

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

func testConn(t *testing.T) *gorm.DB {
	t.Helper()

	conn, err := appdb.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory test database: %v", err)
	}

	if err := appdb.Migrate(conn); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}

	return conn
}

// seedUser creates a user row and returns its local ID.
func seedUser(t *testing.T, conn *gorm.DB, gitlabUserID int64, username string, exp int64) int64 {
	t.Helper()

	user := appdb.User{GitLabUserID: gitlabUserID, Username: username, ExpTotal: exp}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user %q: %v", username, err)
	}

	return user.ID
}

// seedAward creates a definition and an award of it to userID.
func seedAward(t *testing.T, conn *gorm.DB, userID int64, criteria string, tier, threshold, exp int64, status appdb.AwardStatus) {
	t.Helper()

	definition := appdb.AchievementDefinition{
		CriteriaKey:         criteria,
		Name:                criteria + " tier",
		Description:         "earned by doing things",
		GitLabAchievementID: tier*1000 + int64(len(criteria)),
		Tier:                tier,
		Threshold:           threshold,
		ExpReward:           exp,
	}
	if err := conn.Create(&definition).Error; err != nil {
		t.Fatalf("failed to seed definition: %v", err)
	}

	award := appdb.Award{
		AwardedAt:               time.Now().UTC(),
		Status:                  status,
		UserID:                  userID,
		AchievementDefinitionID: definition.ID,
	}
	if err := conn.Create(&award).Error; err != nil {
		t.Fatalf("failed to seed award: %v", err)
	}
}

// seedCounter creates a progress counter for userID.
func seedCounter(t *testing.T, conn *gorm.DB, userID int64, criteria string, count int64) {
	t.Helper()

	counter := appdb.ProgressCounter{CriteriaKey: criteria, UserID: userID, Count: count}
	if err := conn.Create(&counter).Error; err != nil {
		t.Fatalf("failed to seed counter: %v", err)
	}
}

func TestLookup_ResolvesByGitLabUserID(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 100)

	s := &store{conn: conn}

	got, err := s.Summary(t.Context(), "42")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got.Username != "alice" {
		t.Errorf("expected to resolve user 42 to alice, got %q", got.Username)
	}
}

func TestLookup_ResolvesByUsername(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 100)

	s := &store{conn: conn}

	got, err := s.Summary(t.Context(), "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got.GitLabUserID != 42 {
		t.Errorf("expected to resolve alice to user 42, got %d", got.GitLabUserID)
	}
}

// A GitLab username may legally be all digits, so an all-digit ref is
// ambiguous. The ID reading wins, since that is what a programmatic caller
// almost always holds.
func TestLookup_PrefersUserIDOverAnAllDigitUsername(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 7, "7", 10)
	seedUser(t, conn, 99, "somebody", 20)

	// Give the user whose *username* is "7" a different GitLab ID, and give
	// the ID 7 to somebody else entirely.
	if err := conn.Model(&appdb.User{}).Where("username = ?", "7").Update("git_lab_user_id", 1234).Error; err != nil {
		t.Fatalf("failed to prepare the collision: %v", err)
	}

	if err := conn.Model(&appdb.User{}).Where("username = ?", "somebody").Update("git_lab_user_id", 7).Error; err != nil {
		t.Fatalf("failed to prepare the collision: %v", err)
	}

	s := &store{conn: conn}

	got, err := s.Summary(t.Context(), "7")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got.Username != "somebody" {
		t.Errorf("expected %q to resolve as a user ID (somebody), got username %q", "7", got.Username)
	}
}

// An all-numeric username still resolves when no user holds that ID, which
// is what makes the ID-first rule safe rather than merely convenient.
func TestLookup_FallsBackToAnAllDigitUsernameWhenNoSuchIDExists(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 500, "1234", 60)

	s := &store{conn: conn}

	got, err := s.Summary(t.Context(), "1234")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got.GitLabUserID != 500 {
		t.Errorf("expected the all-digit username to resolve, got user %d", got.GitLabUserID)
	}
}

// "0" is not a valid GitLab user ID, and must not be allowed to build a
// query with no WHERE clause at all.
func TestLookup_DoesNotMatchAnArbitraryUserForRefZero(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 100)

	s := &store{conn: conn}

	_, err := s.Summary(t.Context(), "0")
	if !errors.Is(err, ErrUserUnknown) {
		t.Errorf("expected ref %q to resolve to nobody, got: %v", "0", err)
	}
}

// The engine rewrites users.username on rename, so a lookup by the current
// name has to find them.
func TestLookup_ResolvesARenamedUserUnderTheirCurrentName(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 100)

	if err := conn.Model(&appdb.User{}).Where("git_lab_user_id = ?", 42).Update("username", "alice-renamed").Error; err != nil {
		t.Fatalf("failed to rename: %v", err)
	}

	s := &store{conn: conn}

	got, err := s.Summary(t.Context(), "alice-renamed")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got.GitLabUserID != 42 {
		t.Errorf("expected the renamed user to resolve, got %d", got.GitLabUserID)
	}
}

func TestLookup_ReportsAnUnknownUser(t *testing.T) {
	s := &store{conn: testConn(t)}

	_, err := s.Summary(t.Context(), "nobody")
	if !errors.Is(err, ErrUserUnknown) {
		t.Errorf("expected ErrUserUnknown, got: %v", err)
	}
}

// A user this app knows who has earned nothing is a different answer from
// a user it has never seen, and the two must not collapse into one.
func TestSummary_KnownUserWithNoEXPIsNotAMiss(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 0)

	s := &store{conn: conn}

	got, err := s.Summary(t.Context(), "alice")
	if err != nil {
		t.Fatalf("expected a known user with zero EXP to resolve, got: %v", err)
	}

	if got.ExpTotal != 0 {
		t.Errorf("expected 0 EXP, got %d", got.ExpTotal)
	}
}

func TestDetail_CarriesCountersAndAwards(t *testing.T) {
	conn := testConn(t)
	userID := seedUser(t, conn, 42, "alice", 300)
	seedCounter(t, conn, userID, "commits", 412)
	seedAward(t, conn, userID, "commits", 3, 100, 300, appdb.AwardStatusAccepted)

	s := &store{conn: conn}

	got, err := s.Detail(t.Context(), "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(got.Counters) != 1 || got.Counters[0].Count != 412 {
		t.Errorf("expected the commits counter, got %+v", got.Counters)
	}

	if len(got.Awards) != 1 {
		t.Fatalf("expected one award, got %d", len(got.Awards))
	}

	award := got.Awards[0]
	if award.CriteriaKey != "commits" || award.Tier != 3 || award.ExpReward != 300 || award.Threshold != 100 {
		t.Errorf("expected the award to carry its definition, got %+v", award)
	}
}

// EXP is paid the moment a tier is earned, whatever has happened to the
// award on GitLab's side since, so a superseded tier is still reported.
func TestDetail_ReportsAwardsOfEveryDeliveryStatus(t *testing.T) {
	conn := testConn(t)
	userID := seedUser(t, conn, 42, "alice", 400)
	seedAward(t, conn, userID, "commits", 1, 10, 100, appdb.AwardStatusSuperseded)
	seedAward(t, conn, userID, "commits", 2, 50, 300, appdb.AwardStatusPending)

	s := &store{conn: conn}

	got, err := s.Detail(t.Context(), "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(got.Awards) != 2 {
		t.Fatalf("expected both tiers regardless of status, got %d", len(got.Awards))
	}

	// Sorted by criteria then tier, so the superseded tier 1 comes first.
	if got.Awards[0].Status != appdb.AwardStatusSuperseded || got.Awards[1].Status != appdb.AwardStatusPending {
		t.Errorf("expected tiers ordered by tier number, got %+v", got.Awards)
	}
}

func TestDetail_EncodesEmptyCollectionsRatherThanNil(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 42, "alice", 0)

	s := &store{conn: conn}

	got, err := s.Detail(t.Context(), "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got.Counters == nil || got.Awards == nil {
		t.Errorf("expected empty slices rather than nil, got counters=%v awards=%v", got.Counters, got.Awards)
	}
}

// An award whose definition has left the catalog describes an achievement
// that no longer exists; it is dropped rather than reported half-populated.
func TestDetail_SkipsAwardsWhoseDefinitionIsGone(t *testing.T) {
	conn := testConn(t)
	userID := seedUser(t, conn, 42, "alice", 0)
	seedAward(t, conn, userID, "commits", 1, 10, 100, appdb.AwardStatusAccepted)

	if err := conn.Where("1 = 1").Delete(&appdb.AchievementDefinition{}).Error; err != nil {
		t.Fatalf("failed to delete definitions: %v", err)
	}

	s := &store{conn: conn}

	got, err := s.Detail(t.Context(), "alice")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(got.Awards) != 0 {
		t.Errorf("expected the orphaned award to be skipped, got %+v", got.Awards)
	}
}

func TestLeaderboard_OrdersByEXPDescending(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 1, "low", 10)
	seedUser(t, conn, 2, "high", 900)
	seedUser(t, conn, 3, "mid", 500)

	s := &store{conn: conn}

	board, err := s.Leaderboard(t.Context(), 10)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	want := []string{"high", "mid", "low"}
	for i, username := range want {
		if board.Entries[i].Username != username {
			t.Errorf("expected rank %d to be %q, got %q", i+1, username, board.Entries[i].Username)
		}

		if board.Entries[i].Rank != i+1 {
			t.Errorf("expected rank %d, got %d", i+1, board.Entries[i].Rank)
		}
	}
}

// Repeated calls have to agree, which means ties cannot be left to
// whatever order the database happens to return.
func TestLeaderboard_BreaksTiesByUserID(t *testing.T) {
	conn := testConn(t)
	seedUser(t, conn, 30, "c", 100)
	seedUser(t, conn, 10, "a", 100)
	seedUser(t, conn, 20, "b", 100)

	s := &store{conn: conn}

	board, err := s.Leaderboard(t.Context(), 10)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	want := []int64{10, 20, 30}
	for i, id := range want {
		if board.Entries[i].GitLabUserID != id {
			t.Errorf("expected rank %d to be user %d, got %d", i+1, id, board.Entries[i].GitLabUserID)
		}
	}
}

func TestLeaderboard_HonorsTheLimit(t *testing.T) {
	conn := testConn(t)

	for i := range int64(5) {
		seedUser(t, conn, i+1, "user", (i+1)*10)
	}

	s := &store{conn: conn}

	board, err := s.Leaderboard(t.Context(), 2)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(board.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(board.Entries))
	}
}
