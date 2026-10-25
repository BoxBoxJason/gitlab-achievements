package bootstrap

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

// fileConn opens a file-backed database, which concurrent passes need:
// ":memory:" gives every pooled connection a private database of its own,
// so two passes would never see each other's rows or leases.
func fileConn(t *testing.T) *gorm.DB {
	t.Helper()

	conn, err := appdb.Open("sqlite://" + filepath.Join(t.TempDir(), "awards.db"))
	if err != nil {
		t.Fatalf("failed to open the test database: %v", err)
	}

	if err := appdb.Migrate(conn); err != nil {
		t.Fatalf("failed to migrate the test database: %v", err)
	}

	return conn
}

// gatedAwardWriter holds the first AwardAchievement call until released,
// which is the window two overlapping passes would race through.
type gatedAwardWriter struct {
	*fakeAchievementWriter

	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
	mu      sync.Mutex
}

func (g *gatedAwardWriter) AwardAchievement(achievementID, userID int64, opt *gitlab.AwardAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error) {
	g.once.Do(func() {
		close(g.entered)
		<-g.proceed
	})

	g.mu.Lock()
	defer g.mu.Unlock()

	return g.fakeAchievementWriter.AwardAchievement(achievementID, userID, opt, options...)
}

// TestReconcileAwards_OverlappingPassesAwardOnce is the regression guard for
// passes scheduled independently of each other (the hourly sweep beside a
// backfill's delivery, say): the second must wait for the first rather than
// read GitLab's holdings before the first has recorded what it awarded.
func TestReconcileAwards_OverlappingPassesAwardOnce(t *testing.T) {
	previous := awardsLeaseOptions
	awardsLeaseOptions = appdb.LeaseOptions{Retry: 10 * time.Millisecond}

	t.Cleanup(func() { awardsLeaseOptions = previous })

	conn := fileConn(t)
	fake := &fakeAchievementWriter{}
	write := &gatedAwardWriter{
		fakeAchievementWriter: fake,
		entered:               make(chan struct{}),
		proceed:               make(chan struct{}),
	}

	user := seedUser(t, conn, fake, "alice", 100)
	def := seedTier(t, conn, "commits", 1, 9)
	award := seedAward(t, conn, user, def, appdb.AwardStatusPending)

	var wg sync.WaitGroup

	errs := make(chan error, 2)
	pass := func() {
		defer wg.Done()

		_, err := ReconcileAwards(t.Context(), write, conn)
		errs <- err
	}

	wg.Add(1)

	go pass()

	<-write.entered

	wg.Add(1)

	go pass()

	// Long enough for an unserialized second pass to list and award.
	time.Sleep(200 * time.Millisecond)
	close(write.proceed)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	}

	if fake.awardCalls != 1 {
		t.Errorf("expected exactly one award across both passes, got %d", fake.awardCalls)
	}

	if live := len(fake.userAwards[user.GitLabUserID]); live != 1 {
		t.Errorf("expected one award on GitLab, got %d", live)
	}

	if got := reloadAward(t, conn, award.ID); got.Status != appdb.AwardStatusAccepted || got.GitLabUserAchievementID == 0 {
		t.Errorf("expected the award delivered and tracked, got %q/%d", got.Status, got.GitLabUserAchievementID)
	}
}

// racingAwardWriter records the award on the database row itself while the
// award is being made, standing in for a second writer the lease didn't
// keep out.
type racingAwardWriter struct {
	*fakeAchievementWriter

	conn    *gorm.DB
	awardID int64
}

func (r *racingAwardWriter) AwardAchievement(achievementID, userID int64, opt *gitlab.AwardAchievementOptions, options ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error) {
	theirs := r.grantAward(achievementID, userID)

	err := r.conn.Model(&appdb.Award{}).Where("id = ?", r.awardID).Updates(map[string]any{
		"status":                      appdb.AwardStatusAccepted,
		"git_lab_user_achievement_id": theirs.ID,
	}).Error
	if err != nil {
		return nil, err
	}

	return r.fakeAchievementWriter.AwardAchievement(achievementID, userID, opt, options...)
}

// TestReconcileAwards_LeavesARowAnotherWriterRecorded covers the second
// line of defence: a pass whose award lands after somebody else recorded
// one must not overwrite their ID, and must take its own duplicate back.
func TestReconcileAwards_LeavesARowAnotherWriterRecorded(t *testing.T) {
	conn := testConn(t)
	fake := &fakeAchievementWriter{}

	user := seedUser(t, conn, fake, "alice", 100)
	def := seedTier(t, conn, "commits", 1, 9)
	award := seedAward(t, conn, user, def, appdb.AwardStatusPending)

	write := &racingAwardWriter{fakeAchievementWriter: fake, conn: conn, awardID: award.ID}

	report, err := ReconcileAwards(t.Context(), write, conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Confirmed != 0 {
		t.Errorf("expected the pass to record nothing over the other writer, got %+v", report)
	}

	awards := fake.userAwards[user.GitLabUserID]
	if len(awards) != 2 {
		t.Fatalf("expected the other writer's award and this pass's, got %d", len(awards))
	}

	theirs, ours := awards[0], awards[1]

	if got := reloadAward(t, conn, award.ID).GitLabUserAchievementID; got != theirs.ID {
		t.Errorf("expected the other writer's ID %d to be kept, got %d", theirs.ID, got)
	}

	if theirs.RevokedAt != nil || ours.RevokedAt == nil {
		t.Errorf("expected only this pass's duplicate revoked, got theirs=%v ours=%v", theirs.RevokedAt != nil, ours.RevokedAt != nil)
	}
}
