package bootstrap

import (
	"errors"
	"io"
	"iter"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

type fakeAchievementWriter struct {
	nextID        int64
	createErr     error
	updateErr     error
	awardErr      error
	listErr       error
	updateCalls   int
	awardCalls    int
	listCalls     int
	avatarUploads int

	// achievements mirrors what GitLab "actually" has on record, keyed by
	// ID. CreateAchievement populates it automatically; tests simulate an
	// out-of-band deletion by deleting straight from this map before
	// calling ReconcileAchievements.
	achievements map[int64]*gitlab.Achievement
}

// simulateAvatarUpload drains upload's content, as the real GraphQL client
// would when streaming a multipart request, and reports the URL GitLab
// would have assigned had this actually been uploaded.
func (f *fakeAchievementWriter) simulateAvatarUpload(upload *gitlab.GraphQLUpload) *string {
	if upload == nil {
		return nil
	}

	f.avatarUploads++
	_, _ = io.Copy(io.Discard, upload.Content)

	url := "https://gitlab.example.com/uploads/" + upload.Filename

	return &url
}

func (f *fakeAchievementWriter) CreateAchievement(_ int64, opt *gitlab.CreateAchievementOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}

	f.nextID++

	name := ""
	if opt.Name != nil {
		name = *opt.Name
	}

	description := ""
	if opt.Description != nil {
		description = *opt.Description
	}

	achievement := &gitlab.Achievement{
		ID:          f.nextID,
		Name:        name,
		Description: &description,
		AvatarURL:   f.simulateAvatarUpload(opt.Avatar),
	}

	if f.achievements == nil {
		f.achievements = make(map[int64]*gitlab.Achievement)
	}

	f.achievements[achievement.ID] = achievement

	return achievement, nil
}

func (f *fakeAchievementWriter) UpdateAchievement(achievementID int64, opt *gitlab.UpdateAchievementOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.Achievement, error) {
	f.updateCalls++

	if f.updateErr != nil {
		return nil, f.updateErr
	}

	if existing, ok := f.achievements[achievementID]; ok {
		existing.Name = *opt.Name
		existing.Description = opt.Description

		if avatarURL := f.simulateAvatarUpload(opt.Avatar); avatarURL != nil {
			existing.AvatarURL = avatarURL
		}
	}

	return &gitlab.Achievement{ID: achievementID, Name: *opt.Name}, nil
}

func (f *fakeAchievementWriter) AwardAchievement(achievementID, userID int64, _ *gitlab.AwardAchievementOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.UserAchievement, error) {
	f.awardCalls++

	if f.awardErr != nil {
		return nil, f.awardErr
	}

	return &gitlab.UserAchievement{AchievementID: achievementID, UserID: userID}, nil
}

func (f *fakeAchievementWriter) ListAchievements(_ string, _ *gitlab.ListAchievementsOptions, _ ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.Achievement, error] {
	f.listCalls++

	return func(yield func(*gitlab.Achievement, error) bool) {
		if f.listErr != nil {
			yield(nil, f.listErr)

			return
		}

		for _, achievement := range f.achievements {
			if !yield(achievement, nil) {
				return
			}
		}
	}
}

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

func TestSyncAchievements_CreatesMissingEntries(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
		{CriteriaKey: "commits", Tier: 2, Threshold: 100, Name: "Committer II", Description: "Made 100 commits."},
	}

	report, err := syncAchievements(t.Context(), write, conn, 42, entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created != 2 || report.Updated != 0 || report.Unchanged != 0 {
		t.Fatalf("expected 2 created, got %+v", report)
	}

	var count int64
	conn.Model(&appdb.AchievementDefinition{}).Count(&count)

	if count != 2 {
		t.Errorf("expected 2 persisted achievement definitions, got %d", count)
	}
}

func TestSyncAchievements_IdempotentOnRepeatRun(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	if _, err := syncAchievements(t.Context(), write, conn, 42, entries); err != nil {
		t.Fatalf("expected no error on first run, got: %v", err)
	}

	report, err := syncAchievements(t.Context(), write, conn, 42, entries)
	if err != nil {
		t.Fatalf("expected no error on second run, got: %v", err)
	}

	if report.Created != 0 || report.Unchanged != 1 {
		t.Fatalf("expected 1 unchanged, 0 created on repeat run, got %+v", report)
	}

	var count int64
	conn.Model(&appdb.AchievementDefinition{}).Count(&count)

	if count != 1 {
		t.Errorf("expected no duplicate rows, got %d", count)
	}
}

func TestSyncAchievements_UpdatesDriftedEntry(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	original := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}
	if _, err := syncAchievements(t.Context(), write, conn, 42, original); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	changed := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits. Renamed."},
	}

	report, err := syncAchievements(t.Context(), write, conn, 42, changed)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Updated != 1 {
		t.Fatalf("expected 1 updated, got %+v", report)
	}

	if write.updateCalls != 1 {
		t.Errorf("expected UpdateAchievement to be called once, got %d", write.updateCalls)
	}

	var def appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&def).Error; err != nil {
		t.Fatalf("failed to load persisted definition: %v", err)
	}

	if def.Description != "Made 10 commits. Renamed." {
		t.Errorf("expected persisted description to match catalog, got %q", def.Description)
	}
}

func TestSyncAchievements_ThresholdOnlyChangeSkipsGitLabCall(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	original := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}
	if _, err := syncAchievements(t.Context(), write, conn, 42, original); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	changed := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 20, Name: "Committer I", Description: "Made 10 commits."},
	}

	report, err := syncAchievements(t.Context(), write, conn, 42, changed)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Updated != 1 {
		t.Fatalf("expected 1 updated, got %+v", report)
	}

	if write.updateCalls != 0 {
		t.Errorf("expected no GitLab-side update for a local-only threshold change, got %d calls", write.updateCalls)
	}

	var def appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&def).Error; err != nil {
		t.Fatalf("failed to load persisted definition: %v", err)
	}

	if def.Threshold != 20 {
		t.Errorf("expected persisted threshold to be updated to 20, got %d", def.Threshold)
	}
}

func TestSyncAchievements_CreateErrorIsNotPersisted(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{createErr: errors.New("permission denied")}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	_, err := syncAchievements(t.Context(), write, conn, 42, entries)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	var count int64
	conn.Model(&appdb.AchievementDefinition{}).Count(&count)

	if count != 0 {
		t.Errorf("expected no row to be persisted after a failed create, got %d", count)
	}
}

func TestCreateAchievement_PersistsGitLabAchievementID(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entry := catalog.Entry{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "desc"}
	if err := createAchievement(t.Context(), write, conn, 42, entry); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var def appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&def).Error; err != nil {
		t.Fatalf("failed to load persisted definition: %v", err)
	}

	if def.GitLabAchievementID == 0 {
		t.Errorf("expected a non-zero GitLabAchievementID, got %d", def.GitLabAchievementID)
	}
}

func TestCreateAchievement_UploadsAvatarWhenConfigured(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entry := catalog.Entry{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "desc", AvatarPath: "assets/commit.png"}
	if err := createAchievement(t.Context(), write, conn, 42, entry); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if write.avatarUploads != 1 {
		t.Errorf("expected 1 avatar upload, got %d", write.avatarUploads)
	}

	var def appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&def).Error; err != nil {
		t.Fatalf("failed to load persisted definition: %v", err)
	}

	if def.AvatarPath != entry.AvatarPath {
		t.Errorf("expected persisted AvatarPath %q, got %q", entry.AvatarPath, def.AvatarPath)
	}
}

func TestCreateAchievement_SkipsAvatarWhenNotConfigured(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entry := catalog.Entry{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "desc"}
	if err := createAchievement(t.Context(), write, conn, 42, entry); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if write.avatarUploads != 0 {
		t.Errorf("expected no avatar upload for an entry without AvatarPath, got %d", write.avatarUploads)
	}
}

func TestSyncAchievements_UploadsAvatarOnceAddedToCatalog(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	withoutAvatar := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}
	if _, err := syncAchievements(t.Context(), write, conn, 42, withoutAvatar); err != nil {
		t.Fatalf("expected no error seeding, got: %v", err)
	}

	withAvatar := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits.", AvatarPath: "assets/commit.png"},
	}

	report, err := syncAchievements(t.Context(), write, conn, 42, withAvatar)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Updated != 1 {
		t.Fatalf("expected 1 updated once an avatar is added to the catalog, got %+v", report)
	}

	if write.avatarUploads != 1 {
		t.Errorf("expected 1 avatar upload, got %d", write.avatarUploads)
	}

	var def appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&def).Error; err != nil {
		t.Fatalf("failed to load persisted definition: %v", err)
	}

	if def.AvatarPath != "assets/commit.png" {
		t.Errorf("expected persisted AvatarPath to be updated, got %q", def.AvatarPath)
	}
}
