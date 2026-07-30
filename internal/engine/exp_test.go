package engine

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// userFor loads the local row for a GitLab user ID.
func userFor(t *testing.T, conn *gorm.DB, gitlabUserID int64) appdb.User {
	t.Helper()

	var user appdb.User
	if err := conn.Where("git_lab_user_id = ?", gitlabUserID).First(&user).Error; err != nil {
		t.Fatalf("failed to load user %d: %v", gitlabUserID, err)
	}

	return user
}

// expFor returns a user's stored EXP total.
func expFor(t *testing.T, conn *gorm.DB, gitlabUserID int64) int64 {
	t.Helper()

	return userFor(t, conn, gitlabUserID).ExpTotal
}

func TestProcess_PaysEveryTierEarned(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 10, exp: 5}, tier{threshold: 100, exp: 20})
	eng := New(conn)

	// One event carrying a user's whole history crosses both tiers, so it
	// has to pay for both, not just the highest.
	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 150)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got := expFor(t, conn, 100); got != 25 {
		t.Errorf("expected 25 EXP for both tiers, got %d", got)
	}
}

func TestProcess_PaysATierOnlyOnce(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 2, exp: 5})
	eng := New(conn)

	for i := range 5 {
		event := commitEvent("project_event:"+string(rune('a'+i))+":commit", 1)
		if err := eng.Process(t.Context(), event); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	if got := expFor(t, conn, 100); got != 5 {
		t.Errorf("expected the tier to pay once however many events keep clearing it, got %d EXP", got)
	}
}

func TestProcess_PaysNothingForAnEventThatEarnsNothing(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 10, exp: 5})
	eng := New(conn)

	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 9)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// The user exists and has progress, but has earned nothing yet: zero
	// EXP and "no such user" are different answers (see the HTTP API).
	if got := expFor(t, conn, 100); got != 0 {
		t.Errorf("expected no EXP below the threshold, got %d", got)
	}
}

func TestProcess_ReplayedActivityDoesNotPayTwice(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 1, exp: 5})
	eng := New(conn)

	event := commitEvent("project_event:1:commit", 1)

	for range 3 {
		if err := eng.Process(t.Context(), event); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	if got := expFor(t, conn, 100); got != 5 {
		t.Errorf("expected a redelivered event to pay once, got %d EXP", got)
	}
}

func TestProcess_PaysForDayDerivedTiers(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaActiveDays, tier{threshold: 2, exp: 9})
	eng := New(conn)

	// The day-derived criteria are recomputed rather than incremented, so
	// their awards run down a different path and have to pay the same.
	processAll(t, eng,
		at(2024, time.May, 1, 10),
		at(2024, time.May, 2, 10),
	)

	if got := expFor(t, conn, 100); got != 9 {
		t.Errorf("expected the active-days tier to pay 9 EXP, got %d", got)
	}
}

func TestProcess_ExpIsPerUser(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 1, exp: 5}, tier{threshold: 10, exp: 20})
	eng := New(conn)

	alice := commitEvent("project_event:1:commit", 10)

	bob := commitEvent("project_event:2:commit", 1)
	bob.ActorID = 200
	bob.ActorUsername = "bob"

	for _, event := range []activity.Event{alice, bob} {
		if err := eng.Process(t.Context(), event); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	if got := expFor(t, conn, 100); got != 25 {
		t.Errorf("expected alice to hold 25 EXP, got %d", got)
	}

	if got := expFor(t, conn, 200); got != 5 {
		t.Errorf("expected bob to hold 5 EXP, got %d", got)
	}
}

func TestProcess_KeepsTheTotalAgreeingWithAFreshRecompute(t *testing.T) {
	// The total the engine maintains as awards land and the total a repair
	// run derives from the awards held are the same number by construction;
	// this is what pins that they stay one code path.
	conn := testConn(t)
	seedDefinitions(t, conn, catalog.CriteriaCommits, 1, 5, 10, 50)
	seedDefinitions(t, conn, catalog.CriteriaActiveDays, 1, 2, 3)
	eng := New(conn)

	processAll(t, eng,
		at(2024, time.May, 1, 10),
		at(2024, time.May, 2, 10),
		at(2024, time.May, 3, 10),
	)

	accumulated := expFor(t, conn, 100)
	if accumulated == 0 {
		t.Fatal("expected the run to have earned something")
	}

	recomputed, err := RecomputeEXP(t.Context(), conn, userFor(t, conn, 100).ID)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if recomputed != accumulated {
		t.Errorf("expected a recompute to agree with the running total %d, got %d", accumulated, recomputed)
	}
}

func TestRecomputeEXP_CountsSupersededTiers(t *testing.T) {
	// Only a user's top tier per criteria is shown on their GitLab profile.
	// The lower ones are hidden, not unearned, so hiding must not quietly
	// deduct what they paid.
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 1, exp: 5}, tier{threshold: 10, exp: 20})
	eng := New(conn)

	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 10)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	total, err := RecomputeEXP(t.Context(), conn, userFor(t, conn, 100).ID)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if total != 25 {
		t.Errorf("expected the superseded tier to keep counting for 25 EXP in total, got %d", total)
	}
}

func TestRecomputeEXP_CountsAwardsWhateverTheirDeliveryStatus(t *testing.T) {
	// An award GitLab hasn't accepted yet, or rejected, is still a tier
	// this app decided was earned. Delivery is reconciliation's problem;
	// making EXP depend on it would make a user's total wobble with the
	// instance's availability.
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 1, exp: 5}, tier{threshold: 10, exp: 20})
	eng := New(conn)

	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 10)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	statuses := []appdb.AwardStatus{appdb.AwardStatusAccepted, appdb.AwardStatusFailed}

	var awards []appdb.Award
	if err := conn.Order("achievement_definition_id").Find(&awards).Error; err != nil {
		t.Fatalf("failed to load awards: %v", err)
	}

	for i, award := range awards {
		if err := conn.Model(&award).Update("status", statuses[i]).Error; err != nil {
			t.Fatalf("failed to set award status: %v", err)
		}
	}

	total, err := RecomputeEXP(t.Context(), conn, userFor(t, conn, 100).ID)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if total != 25 {
		t.Errorf("expected both awards to count for 25 EXP, got %d", total)
	}
}

func TestRecomputeAll_PicksUpARetunedCatalog(t *testing.T) {
	conn := testConn(t)
	definitions := seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 1, exp: 5})
	eng := New(conn)

	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 1)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// A release retunes what the tier is worth. Nobody earns anything for
	// it, so only a sweep can bring the holders' totals along.
	if err := conn.Model(&definitions[0]).Update("exp_reward", 50).Error; err != nil {
		t.Fatalf("failed to retune the definition: %v", err)
	}

	corrected, err := RecomputeAll(t.Context(), conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if corrected != 1 {
		t.Errorf("expected 1 user's total to be corrected, got %d", corrected)
	}

	if got := expFor(t, conn, 100); got != 50 {
		t.Errorf("expected the retuned reward to be reflected as 50 EXP, got %d", got)
	}
}

func TestRecomputeAll_LeavesCorrectTotalsAlone(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 1, exp: 5})
	eng := New(conn)

	if err := eng.Process(t.Context(), commitEvent("project_event:1:commit", 1)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	corrected, err := RecomputeAll(t.Context(), conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if corrected != 0 {
		t.Errorf("expected an already-correct total to be left alone, got %d corrections", corrected)
	}
}

func TestRecomputeAll_ClearsATotalWithNoAwardsBehindIt(t *testing.T) {
	// A total is a cache of what the awards add up to, so a user holding
	// none settles at zero rather than keeping whatever was last written.
	conn := testConn(t)

	user := appdb.User{GitLabUserID: 100, Username: "alice", ExpTotal: 999}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	corrected, err := RecomputeAll(t.Context(), conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if corrected != 1 {
		t.Errorf("expected the stale total to be corrected, got %d corrections", corrected)
	}

	if got := expFor(t, conn, 100); got != 0 {
		t.Errorf("expected a user with no awards to hold 0 EXP, got %d", got)
	}
}

func TestRecomputeAll_ScopesEachUsersTotalToTheirOwnAwards(t *testing.T) {
	conn := testConn(t)
	seedTiers(t, conn, catalog.CriteriaCommits, tier{threshold: 1, exp: 5}, tier{threshold: 10, exp: 20})
	eng := New(conn)

	alice := commitEvent("project_event:1:commit", 10)

	bob := commitEvent("project_event:2:commit", 1)
	bob.ActorID = 200
	bob.ActorUsername = "bob"

	for _, event := range []activity.Event{alice, bob} {
		if err := eng.Process(t.Context(), event); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	// Wipe both totals so the sweep has to rebuild them from the awards.
	if err := conn.Model(&appdb.User{}).Where("1 = 1").Update("exp_total", 0).Error; err != nil {
		t.Fatalf("failed to clear totals: %v", err)
	}

	corrected, err := RecomputeAll(t.Context(), conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if corrected != 2 {
		t.Errorf("expected both users' totals to be rebuilt, got %d corrections", corrected)
	}

	if got := expFor(t, conn, 100); got != 25 {
		t.Errorf("expected alice to hold 25 EXP, got %d", got)
	}

	if got := expFor(t, conn, 200); got != 5 {
		t.Errorf("expected bob to hold 5 EXP, got %d", got)
	}
}
