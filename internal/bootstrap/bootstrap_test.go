package bootstrap

import (
	"errors"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
)

type fakeWriteAll struct {
	fakeWriteVerifier
	fakeAchievementWriter
	fakeHookManager
}

func testCfg() *config.Config {
	return &config.Config{
		AchievementsNamespace: "achievements",
		WebhookSecret:         "s3cr3t",
	}
}

func TestRun_Success(t *testing.T) {
	conn := testConn(t)

	clients := Client{
		Read: validReadVerifier(),
		Write: &fakeWriteAll{
			fakeWriteVerifier: *validWriteVerifier(),
		},
	}

	report, err := Run(clients, conn, testCfg(), "https://achievements.example.com/webhooks/system")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.NamespaceID != 42 {
		t.Errorf("expected namespace id 42, got %d", report.NamespaceID)
	}

	if report.Achievements.Created == 0 {
		t.Errorf("expected at least one achievement to be created, got %+v", report.Achievements)
	}

	if !report.Webhook.Created {
		t.Errorf("expected the webhook to be created")
	}
}

func TestRun_StopsAtPermissionFailure(t *testing.T) {
	conn := testConn(t)

	badWrite := validWriteVerifier()
	badWrite.currentUserErr = errors.New("401 unauthorized")

	writeAll := &fakeWriteAll{fakeWriteVerifier: *badWrite}

	clients := Client{
		Read:  validReadVerifier(),
		Write: writeAll,
	}

	_, err := Run(clients, conn, testCfg(), "https://achievements.example.com/webhooks/system")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if writeAll.addCalls != 0 {
		t.Errorf("expected no webhook registration attempt after a permission failure, got %d calls", writeAll.addCalls)
	}

	var count int64
	conn.Model(&appdb.AchievementDefinition{}).Count(&count)

	if count != 0 {
		t.Errorf("expected no achievements to be created after a permission failure, got %d", count)
	}
}

func TestRun_StopsAtAchievementFailure(t *testing.T) {
	conn := testConn(t)

	writeAll := &fakeWriteAll{
		fakeWriteVerifier:     *validWriteVerifier(),
		fakeAchievementWriter: fakeAchievementWriter{createErr: errors.New("permission denied")},
	}

	clients := Client{
		Read:  validReadVerifier(),
		Write: writeAll,
	}

	_, err := Run(clients, conn, testCfg(), "https://achievements.example.com/webhooks/system")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if writeAll.addCalls != 0 {
		t.Errorf("expected no webhook registration attempt after an achievement sync failure, got %d calls", writeAll.addCalls)
	}
}
