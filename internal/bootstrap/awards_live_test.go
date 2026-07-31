package bootstrap

import (
	"fmt"
	"os"
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

// TestLiveAwardDelivery runs the award delivery path against a real GitLab
// instance, which is the only place several of its assumptions can actually
// be checked: that awarding is not idempotent, that awards land unaccepted,
// that an award can be revoked by the token that made it, and that hidden
// awards are listed back to that token. A fake can be made to agree with
// any of those; GitLab is what decides.
//
// It skips unless LIVE_GITLAB_URL and LIVE_GITLAB_TOKEN name a throwaway
// instance and an admin token on it. The run leaves a group, a user and
// four achievements behind, so point it at an instance you can discard.
// See docs/achievements-api-behavior.md for what it was written from, and
// for how to bring such an instance up.
func TestLiveAwardDelivery(t *testing.T) {
	baseURL := os.Getenv("LIVE_GITLAB_URL")
	token := os.Getenv("LIVE_GITLAB_TOKEN")
	if baseURL == "" || token == "" {
		t.Skip("set LIVE_GITLAB_URL and LIVE_GITLAB_TOKEN to run against a throwaway GitLab instance")
	}

	write, err := gitlabclient.NewWriteClient(baseURL, token)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := gitlab.NewClient(token, gitlab.WithBaseURL(baseURL))
	if err != nil {
		t.Fatal(err)
	}

	stamp := fmt.Sprintf("%d", time.Now().Unix())

	group, _, err := raw.Groups.CreateGroup(&gitlab.CreateGroupOptions{
		Name: gitlab.Ptr("ns" + stamp), Path: gitlab.Ptr("ns" + stamp),
	})
	if err != nil {
		t.Fatal(err)
	}

	glUser, _, err := raw.Users.CreateUser(&gitlab.CreateUserOptions{
		Email:            gitlab.Ptr("live" + stamp + "@example.com"),
		Username:         gitlab.Ptr("live" + stamp),
		Name:             gitlab.Ptr("Live " + stamp),
		Password:         gitlab.Ptr("qR7mVx2nZt9wLc4B"),
		SkipConfirmation: gitlab.Ptr(true),
	})
	if err != nil {
		t.Fatal(err)
	}

	conn := testConn(t)

	user := appdb.User{Username: glUser.Username, GitLabUserID: int64(glUser.ID)}
	if err := conn.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	// Four tiers of one criteria, as real GitLab achievements.
	defs := make([]appdb.AchievementDefinition, 0, 4)

	for tier := int64(1); tier <= 4; tier++ {
		name := fmt.Sprintf("Live Committer %d (%s)", tier, stamp)

		achievement, achErr := write.CreateAchievement(int64(group.ID), &gitlab.CreateAchievementOptions{
			Name: &name, Description: gitlab.Ptr("live probe"),
		})
		if achErr != nil {
			t.Fatal(achErr)
		}

		def := appdb.AchievementDefinition{
			GitLabAchievementID: achievement.ID,
			CriteriaKey:         "commits",
			Tier:                tier,
			Name:                name,
			Threshold:           tier * 10,
		}
		if err := conn.Create(&def).Error; err != nil {
			t.Fatal(err)
		}

		defs = append(defs, def)
	}

	liveHeld := func() []*gitlab.UserAchievement {
		t.Helper()

		var held []*gitlab.UserAchievement

		includeHidden := true
		for ua, listErr := range write.ListUserAchievements(user.Username, &gitlab.ListUserAchievementsOptions{IncludeHidden: &includeHidden}) {
			if listErr != nil {
				t.Fatal(listErr)
			}

			if ua.RevokedAt == nil {
				held = append(held, ua)
			}
		}

		return held
	}

	award := func(def appdb.AchievementDefinition) appdb.Award {
		t.Helper()

		row := appdb.Award{UserID: user.ID, AchievementDefinitionID: def.ID, Status: appdb.AwardStatusPending}
		if err := conn.Create(&row).Error; err != nil {
			t.Fatal(err)
		}

		return row
	}

	// Pass 1: three tiers reached at once, as a backfill would produce.
	for _, def := range defs[:3] {
		award(def)
	}

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("pass 1 report: %+v", report)

	held := liveHeld()
	if len(held) != 1 {
		t.Fatalf("pass 1: expected exactly one live award, got %d: %+v", len(held), held)
	}

	if held[0].AchievementID != defs[2].GitLabAchievementID {
		t.Errorf("pass 1: expected tier 3 on GitLab, got achievement %d", held[0].AchievementID)
	}

	if held[0].ShowOnProfile {
		t.Error("pass 1: expected a fresh award to be unaccepted on GitLab")
	}

	// Pass 2: idempotence. Nothing new should reach GitLab.
	report, err = ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("pass 2 report: %+v", report)

	if held = liveHeld(); len(held) != 1 {
		t.Fatalf("pass 2: expected still exactly one live award, got %d", len(held))
	}

	// Pass 3: promotion to tier 4 withdraws tier 3.
	award(defs[3])

	report, err = ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("pass 3 report: %+v", report)

	held = liveHeld()
	if len(held) != 1 {
		t.Fatalf("pass 3: expected exactly one live award after promotion, got %d: %+v", len(held), held)
	}

	if held[0].AchievementID != defs[3].GitLabAchievementID {
		t.Errorf("pass 3: expected tier 4 on GitLab, got achievement %d", held[0].AchievementID)
	}

	// Pass 4: a delivered award whose ID this app lost must be adopted, not
	// awarded a second time.
	if err := conn.Model(&appdb.Award{}).
		Where("achievement_definition_id = ?", defs[3].ID).
		Update("git_lab_user_achievement_id", 0).Error; err != nil {
		t.Fatal(err)
	}

	report, err = ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("pass 4 report: %+v", report)

	if report.Adopted != 1 {
		t.Errorf("pass 4: expected the lost award to be adopted, got %+v", report)
	}

	held = liveHeld()
	if len(held) != 1 {
		t.Fatalf("pass 4: expected no duplicate award, got %d: %+v", len(held), held)
	}
}
