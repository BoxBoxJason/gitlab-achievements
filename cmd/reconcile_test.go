package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
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

func TestBuildRootCmd_BindsReconcileFlags(t *testing.T) {
	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.ParseFlags([]string{
		"--reconcile", "off",
		"--reconcile-interval", "6h",
		"--reconcile-lookback", "12h",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.ReconcileMode != "off" || cfg.ReconcileInterval != 6*time.Hour || cfg.ReconcileLookback != 12*time.Hour {
		t.Errorf("expected the reconcile flags to be bound, got %+v", cfg)
	}
}

func TestBuildRootCmd_ReconcileSubcommandSharesTheServerFlags(t *testing.T) {
	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	reconcileCmd, _, err := rootCmd.Find([]string{"reconcile"})
	if err != nil {
		t.Fatalf("expected a reconcile subcommand, got: %v", err)
	}

	if reconcileCmd.Name() != "reconcile" {
		t.Fatalf("expected the reconcile subcommand, got %q", reconcileCmd.Name())
	}

	// The scheduled job has to be configurable exactly like the server,
	// from the same flags and environment variables.
	err = reconcileCmd.ParseFlags([]string{
		"--gitlab-url", "https://gitlab.example.com",
		"--database-dsn", "sqlite://:memory:",
		"--reconcile-lookback", "72h",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.GitLabURL != "https://gitlab.example.com" || cfg.ReconcileLookback != 72*time.Hour {
		t.Errorf("expected the subcommand to bind the shared flags, got %+v", cfg)
	}
}

func TestRunReconcile_InvalidConfig(t *testing.T) {
	err := runReconcile(&config.Config{})
	if err == nil {
		t.Fatal("expected an error for an empty config, got nil")
	}

	if !strings.Contains(err.Error(), "invalid configuration") {
		t.Errorf("expected error to mention invalid configuration, got: %v", err)
	}
}

// A scheduled `reconcile` against a database nothing has ever bootstrapped
// should say so, rather than read the whole instance and award nothing
// because there are no definitions to award against.
func TestRequireBootstrapped(t *testing.T) {
	conn := testConn(t)

	err := requireBootstrapped(conn)
	if !errors.Is(err, errNotBootstrapped) {
		t.Fatalf("expected an unbootstrapped database to be reported, got: %v", err)
	}

	definition := appdb.AchievementDefinition{
		GitLabAchievementID: 1, CriteriaKey: "commits", Name: "Committer I", Tier: 1, Threshold: 10,
	}
	if err := conn.Create(&definition).Error; err != nil {
		t.Fatalf("failed to seed an achievement definition: %v", err)
	}

	if err := requireBootstrapped(conn); err != nil {
		t.Errorf("expected a bootstrapped database to pass, got: %v", err)
	}
}

// Reconciliation is the steady-state correction on top of a complete
// picture, so it waits for the historical walk to have produced one rather
// than putting a second instance-wide read sweep alongside the cold start.
func TestHistoryWalked(t *testing.T) {
	conn := testConn(t)

	walked, err := historyWalked(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if walked {
		t.Error("expected an un-walked instance to report so")
	}

	state := appdb.SyncState{Key: "backfill:completed_at", Value: time.Now().UTC().Format(time.RFC3339)}
	if err := conn.Create(&state).Error; err != nil {
		t.Fatalf("failed to seed the backfill watermark: %v", err)
	}

	walked, err = historyWalked(conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !walked {
		t.Error("expected a walked instance to report so")
	}
}

func TestEnvDurationOrDefault(t *testing.T) {
	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_INTERVAL", "")

	if got := envDurationOrDefault("GITLAB_ACHIEVEMENTS_TEST_INTERVAL", time.Hour); got != time.Hour {
		t.Errorf("expected the fallback, got %v", got)
	}

	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_INTERVAL", "90m")

	if got := envDurationOrDefault("GITLAB_ACHIEVEMENTS_TEST_INTERVAL", time.Hour); got != 90*time.Minute {
		t.Errorf("expected the env value, got %v", got)
	}

	// An unreadable value defers to Validate, which names the flag.
	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_INTERVAL", "nightly")

	if got := envDurationOrDefault("GITLAB_ACHIEVEMENTS_TEST_INTERVAL", time.Hour); got != time.Hour {
		t.Errorf("expected the fallback for an unreadable value, got %v", got)
	}
}

// The ticker's phase is the process's start time, so waiting a full
// interval for the first pass means a deployment restarted more often than
// the interval never reconciles at all — silently, since a pass that never
// runs logs nothing.
func TestStartupDelay(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{
			name:     "the default interval waits out the startup burst",
			interval: config.DefaultReconcileInterval,
			want:     reconcileStartupDelay,
		},
		{
			name:     "an interval shorter than the delay is not overrun by it",
			interval: time.Minute,
			want:     time.Minute,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := startupDelay(tc.interval); got != tc.want {
				t.Errorf("expected %v, got %v", tc.want, got)
			}
		})
	}

	// A pass at every restart is only affordable because it is bounded: it
	// must never be the interval itself, or the hole it exists to close
	// reopens.
	if startupDelay(config.DefaultReconcileInterval) >= config.DefaultReconcileInterval {
		t.Error("expected the first pass well before a full interval, or a restarting pod never reconciles")
	}
}

func TestSleepOrDone(t *testing.T) {
	if !sleepOrDone(t.Context(), time.Millisecond) {
		t.Error("expected an elapsed wait to report so")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if sleepOrDone(ctx, time.Hour) {
		t.Error("expected a cancelled context to cut the wait short")
	}
}
