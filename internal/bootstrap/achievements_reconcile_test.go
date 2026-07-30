package bootstrap

import (
	"errors"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

func TestReconcileAchievements_CreatesMissingEntries(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created != 1 {
		t.Fatalf("expected 1 created, got %+v", report)
	}
}

func TestReconcileAchievements_UnchangedEntryIsNotTouched(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	if _, err := syncAchievements(t.Context(), write, conn, 42, entries); err != nil {
		t.Fatalf("expected no error seeding via bootstrap sync, got: %v", err)
	}

	// The achievement is still present on GitLab's side (write.achievements
	// still has it) and nothing about the catalog entry changed, so this
	// should be a no-op beyond the existence-listing call.
	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Unchanged != 1 || report.Recreated != 0 || report.Updated != 0 {
		t.Fatalf("expected 1 unchanged, got %+v", report)
	}

	if write.listCalls != 1 {
		t.Errorf("expected ListAchievements to be called once, got %d", write.listCalls)
	}

	if write.updateCalls != 0 {
		t.Errorf("expected no UpdateAchievement call for an unchanged entry, got %d", write.updateCalls)
	}
}

func TestReconcileAchievements_UpdatesDriftedEntry(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	original := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}
	if _, err := syncAchievements(t.Context(), write, conn, 42, original); err != nil {
		t.Fatalf("expected no error seeding via bootstrap sync, got: %v", err)
	}

	changed := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits. Renamed."},
	}

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", changed)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Updated != 1 {
		t.Fatalf("expected 1 updated, got %+v", report)
	}
}

func TestReconcileAchievements_HealsDriftMadeDirectlyOnGitLab(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	if _, err := syncAchievements(t.Context(), write, conn, 42, entries); err != nil {
		t.Fatalf("expected no error seeding via bootstrap sync, got: %v", err)
	}

	var seeded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	// Simulate someone hand-editing the achievement directly on GitLab: the
	// local row still matches the catalog, but GitLab's own record has
	// drifted.
	live := write.achievements[seeded.GitLabAchievementID]
	live.Name = "Renamed by hand"
	renamedDescription := "Edited outside the app."
	live.Description = &renamedDescription

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Updated != 1 {
		t.Fatalf("expected 1 updated because GitLab's record drifted from the catalog, got %+v", report)
	}

	if write.updateCalls != 1 {
		t.Errorf("expected UpdateAchievement to be called once to heal the drift, got %d", write.updateCalls)
	}

	if live.Name != entries[0].Name {
		t.Errorf("expected GitLab's record to be pushed back to %q, got %q", entries[0].Name, live.Name)
	}

	var reloaded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&reloaded).Error; err != nil {
		t.Fatalf("failed to reload definition: %v", err)
	}

	if reloaded.Name != entries[0].Name || reloaded.Description != entries[0].Description {
		t.Errorf("expected local row to stay in sync with the catalog, got %+v", reloaded)
	}
}

func TestReconcileAchievements_ReuploadsAvatarClearedOnGitLab(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits.", AvatarPath: "assets/committer_i.png"},
	}

	if _, err := syncAchievements(t.Context(), write, conn, 42, entries); err != nil {
		t.Fatalf("expected no error seeding via bootstrap sync, got: %v", err)
	}

	var seeded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	// Simulate the achievement's avatar having been cleared directly on
	// GitLab's side: the achievement itself is still present, but
	// AvatarURL now comes back nil even though the catalog expects one.
	write.achievements[seeded.GitLabAchievementID].AvatarURL = nil

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Updated != 1 {
		t.Fatalf("expected 1 updated because the live avatar was missing, got %+v", report)
	}

	if write.avatarUploads != 2 {
		t.Errorf("expected 2 avatar uploads total (initial create + reconcile heal), got %d", write.avatarUploads)
	}

	if write.achievements[seeded.GitLabAchievementID].AvatarURL == nil {
		t.Error("expected the avatar to be re-uploaded, still nil")
	}
}

func TestReconcileAchievements_RecreatesDeletedAchievement(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	if _, err := syncAchievements(t.Context(), write, conn, 42, entries); err != nil {
		t.Fatalf("expected no error seeding via bootstrap sync, got: %v", err)
	}

	var seeded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&seeded).Error; err != nil {
		t.Fatalf("failed to load seeded definition: %v", err)
	}

	// Simulate the achievement having been deleted on GitLab's side: it no
	// longer shows up in ListAchievements.
	delete(write.achievements, seeded.GitLabAchievementID)

	report, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Recreated != 1 {
		t.Fatalf("expected 1 recreated, got %+v", report)
	}

	var reloaded appdb.AchievementDefinition
	if err := conn.Where("criteria_key = ? AND tier = ?", "commits", 1).First(&reloaded).Error; err != nil {
		t.Fatalf("failed to reload definition: %v", err)
	}

	if reloaded.ID != seeded.ID {
		t.Errorf("expected the existing row to be updated in place, got a different row id %d vs %d", reloaded.ID, seeded.ID)
	}

	if reloaded.GitLabAchievementID == seeded.GitLabAchievementID {
		t.Errorf("expected the GitLab achievement id to change after recreation, still %d", reloaded.GitLabAchievementID)
	}

	var count int64
	conn.Model(&appdb.AchievementDefinition{}).Count(&count)

	if count != 1 {
		t.Errorf("expected no duplicate row after recreation, got %d", count)
	}
}

func TestReconcileAchievements_PropagatesListError(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	entries := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}

	if _, err := syncAchievements(t.Context(), write, conn, 42, entries); err != nil {
		t.Fatalf("expected no error seeding via bootstrap sync, got: %v", err)
	}

	write.listErr = errors.New("gitlab unavailable")

	_, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", entries)
	if err == nil {
		t.Fatal("expected an error when ListAchievements fails, got nil")
	}
}

func TestReconcileAchievements_PropagatesUnexpectedUpdateError(t *testing.T) {
	conn := testConn(t)
	write := &fakeAchievementWriter{}

	original := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits."},
	}
	if _, err := syncAchievements(t.Context(), write, conn, 42, original); err != nil {
		t.Fatalf("expected no error seeding via bootstrap sync, got: %v", err)
	}

	changed := []catalog.Entry{
		{CriteriaKey: "commits", Tier: 1, Threshold: 10, Name: "Committer I", Description: "Made 10 commits. Renamed."},
	}

	// A generic failure should be surfaced when pushing local drift.
	write.updateErr = errors.New("500 internal server error")

	_, err := ReconcileAchievements(t.Context(), write, conn, 42, "achievements", changed)
	if err == nil {
		t.Fatal("expected an error for a failed UpdateAchievement call, got nil")
	}
}
