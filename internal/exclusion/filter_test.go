package exclusion_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
)

type fakeLookup struct {
	bots  map[int64]bool
	err   error
	mu    sync.Mutex
	calls int
}

func (f *fakeLookup) IsBot(_ context.Context, gitlabUserID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.err != nil {
		return false, f.err
	}

	return f.bots[gitlabUserID], nil
}

func openDB(t *testing.T) *gorm.DB {
	t.Helper()

	conn, err := db.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatalf("failed to access database: %v", err)
	}

	// One connection, so every query sees the same in-memory database.
	sqlDB.SetMaxOpenConns(1)

	err = db.Migrate(conn)
	if err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	return conn
}

func loadUser(t *testing.T, conn *gorm.DB, gitlabUserID int64) *db.User {
	t.Helper()

	var user db.User

	err := conn.Where(&db.User{GitLabUserID: gitlabUserID}).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}

	if err != nil {
		t.Fatalf("failed to load user: %v", err)
	}

	return &user
}

func TestFilter_Check_AsksGitLabOnceAndCachesTheVerdict(t *testing.T) {
	conn := openDB(t)
	lookup := &fakeLookup{bots: map[int64]bool{7: true}}
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, nil, ""), lookup, true)

	for range 3 {
		reason, err := filter.Check(t.Context(), 7, "deploy-token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if reason != exclusion.Bot {
			t.Fatalf("expected the flagged account to be excluded as a bot, got %q", reason)
		}
	}

	if lookup.calls != 1 {
		t.Fatalf("expected GitLab to be asked once, got %d calls", lookup.calls)
	}

	user := loadUser(t, conn, 7)
	if user == nil || !user.IsBot || user.BotCheckedAt == nil || user.ExcludedReason != string(exclusion.Bot) {
		t.Fatalf("expected the verdict to be stored on the user, got %+v", user)
	}
}

func TestFilter_Check_FallsBackToTheUsernameWhenGitLabCannotAnswer(t *testing.T) {
	conn := openDB(t)
	lookup := &fakeLookup{err: errors.New("gitlab unreachable")}
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, nil, ""), lookup, true)

	reason, err := filter.Check(t.Context(), 8, "project_3_bot_abc123")
	if err != nil {
		t.Fatalf("expected a failed lookup not to be an error, got %v", err)
	}

	if reason != exclusion.Bot {
		t.Fatalf("expected the username pattern to still exclude the bot, got %q", reason)
	}

	reason, err = filter.Check(t.Context(), 9, "alice")
	if err != nil || reason != exclusion.Included {
		t.Fatalf("expected an unverifiable human to be included for now, got %q, %v", reason, err)
	}

	if user := loadUser(t, conn, 9); user != nil {
		t.Fatalf("expected nothing stored for an unchecked participant, got %+v", user)
	}

	lookup.err = nil

	_, _ = filter.Check(t.Context(), 9, "alice")

	if lookup.calls != 3 {
		t.Fatalf("expected an unchecked account to be asked about again, got %d calls", lookup.calls)
	}
}

func TestFilter_Apply_ReclassifiesUnderANewPolicy(t *testing.T) {
	conn := openDB(t)

	for _, user := range []db.User{
		{GitLabUserID: 1, Username: "alice"},
		{GitLabUserID: 2, Username: "renovate"},
		{GitLabUserID: 3, Username: "bob", ExcludedReason: string(exclusion.Configured)},
	} {
		err := conn.Create(&user).Error
		if err != nil {
			t.Fatalf("failed to seed user: %v", err)
		}
	}

	filter := exclusion.NewFilter(conn, mustPolicy(t, true, []string{"renovate"}, ""), nil, true)

	changed, err := filter.Apply(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if changed != 2 {
		t.Errorf("expected renovate excluded and bob let back in, got %d changes", changed)
	}

	if got := loadUser(t, conn, 2).ExcludedReason; got != string(exclusion.Configured) {
		t.Errorf("expected renovate to be excluded, got %q", got)
	}

	if got := loadUser(t, conn, 3).ExcludedReason; got != "" {
		t.Errorf("expected bob, no longer listed, to take part again, got %q", got)
	}
}

func TestFilter_SweepBots_ClassifiesEveryUncheckedUser(t *testing.T) {
	conn := openDB(t)

	for id := int64(1); id <= 450; id++ {
		err := conn.Create(&db.User{GitLabUserID: id, Username: "user"}).Error
		if err != nil {
			t.Fatalf("failed to seed user: %v", err)
		}
	}

	lookup := &fakeLookup{bots: map[int64]bool{17: true, 401: true}}
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, nil, ""), lookup, true)

	excluded, err := filter.SweepBots(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if excluded != 2 || lookup.calls != 450 {
		t.Fatalf("expected 2 bots out of 450 lookups, got %d bots and %d lookups", excluded, lookup.calls)
	}

	excluded, err = filter.SweepBots(t.Context())
	if err != nil || excluded != 0 || lookup.calls != 450 {
		t.Fatalf("expected a second sweep to have nothing left to ask about, got %d bots, %d lookups, %v", excluded, lookup.calls, err)
	}
}

func TestFilter_OptOutAndBackIn(t *testing.T) {
	conn := openDB(t)
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, nil, ""), nil, true)

	err := filter.OptOut(t.Context(), 21, "dana")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reason, optedOutAt, err := filter.Status(t.Context(), 21, "dana")
	if err != nil || reason != exclusion.OptedOut || optedOutAt == nil {
		t.Fatalf("expected dana to be opted out, got %q at %v (%v)", reason, optedOutAt, err)
	}

	checked, err := filter.Check(t.Context(), 21, "dana")
	if err != nil || checked != exclusion.OptedOut {
		t.Fatalf("expected activity from dana to be excluded, got %q (%v)", checked, err)
	}

	err = filter.OptIn(t.Context(), 21, "dana")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reason, optedOutAt, err = filter.Status(t.Context(), 21, "dana")
	if err != nil || reason != exclusion.Included || optedOutAt != nil {
		t.Fatalf("expected dana to take part again, got %q at %v (%v)", reason, optedOutAt, err)
	}
}

func TestFilter_OptOutDisabled(t *testing.T) {
	conn := openDB(t)
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, nil, ""), nil, false)

	err := filter.OptOut(t.Context(), 21, "dana")
	if !errors.Is(err, exclusion.ErrOptOutDisabled) {
		t.Fatalf("expected opt-out to be refused, got %v", err)
	}

	err = filter.OptIn(t.Context(), 21, "dana")
	if err != nil {
		t.Fatalf("expected opting back in to stay available, got %v", err)
	}
}

func TestFilter_OptInCreatesNobody(t *testing.T) {
	conn := openDB(t)
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, nil, ""), nil, true)

	err := filter.OptIn(t.Context(), 21, "dana")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var count int64

	err = conn.Model(&db.User{}).Count(&count).Error
	if err != nil || count != 0 {
		t.Fatalf("expected opting in an unseen user to record nothing, got %d rows (%v)", count, err)
	}
}

func TestFilter_StatusOfUnseenListedUser(t *testing.T) {
	conn := openDB(t)
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, []string{"renovate"}, ""), nil, true)

	reason, optedOutAt, err := filter.Status(t.Context(), 21, "renovate")
	if err != nil || reason != exclusion.Configured || optedOutAt != nil {
		t.Fatalf("expected an unseen listed user to be reported excluded, got %q at %v (%v)", reason, optedOutAt, err)
	}
}

func TestFilter_CheckSkipsLookupForListedUser(t *testing.T) {
	conn := openDB(t)
	lookup := &fakeLookup{}
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, []string{"renovate"}, ""), lookup, true)

	reason, err := filter.Check(t.Context(), 21, "renovate")
	if err != nil || reason != exclusion.Configured || lookup.calls != 0 {
		t.Fatalf("expected a listed user to be excluded without asking gitlab, got %q after %d lookups (%v)", reason, lookup.calls, err)
	}
}

// TestFilter_Check_AppliesUsernameRulesWhateverTheID covers a payload naming
// an actor ID no GitLab user has: there is no row to consult, but the
// username rules must still hold rather than wave the activity through.
func TestFilter_Check_AppliesUsernameRulesWhateverTheID(t *testing.T) {
	conn := openDB(t)
	filter := exclusion.NewFilter(conn, mustPolicy(t, true, []string{"renovate"}, ""), nil, true)

	for username, want := range map[string]exclusion.Reason{
		"renovate":           exclusion.Configured,
		"project_4_bot_beef": exclusion.Bot,
		"alice":              exclusion.Included,
	} {
		got, err := filter.Check(t.Context(), -7, username)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != want {
			t.Errorf("%s: expected %q, got %q", username, want, got)
		}
	}
}
