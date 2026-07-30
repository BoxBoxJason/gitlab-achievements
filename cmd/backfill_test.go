package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
)

func TestBuildRootCmd_BindsBackfillFlags(t *testing.T) {
	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.ParseFlags([]string{
		"--backfill", "force",
		"--backfill-since", "2024-01-01",
		"--backfill-rate", "2.5",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.BackfillMode != "force" || cfg.BackfillSince != "2024-01-01" || cfg.BackfillRate != 2.5 {
		t.Errorf("expected the backfill flags to be bound, got %+v", cfg)
	}
}

func TestBuildRootCmd_BackfillSubcommandSharesTheServerFlags(t *testing.T) {
	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	backfillCmd, _, err := rootCmd.Find([]string{"backfill"})
	if err != nil {
		t.Fatalf("expected a backfill subcommand, got: %v", err)
	}

	if backfillCmd.Name() != "backfill" {
		t.Fatalf("expected the backfill subcommand, got %q", backfillCmd.Name())
	}

	// The one-off job has to be configurable exactly like the server, from
	// the same flags and environment variables.
	err = backfillCmd.ParseFlags([]string{
		"--gitlab-url", "https://gitlab.example.com",
		"--database-dsn", "sqlite://:memory:",
		"--backfill", "force",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.GitLabURL != "https://gitlab.example.com" || cfg.DatabaseDSN != "sqlite://:memory:" || cfg.BackfillMode != "force" {
		t.Errorf("expected the subcommand to bind the shared flags, got %+v", cfg)
	}
}

func TestRunBackfill_InvalidConfig(t *testing.T) {
	err := runBackfill(&config.Config{})
	if err == nil {
		t.Fatal("expected an error for an empty config, got nil")
	}

	if !strings.Contains(err.Error(), "invalid configuration") {
		t.Errorf("expected error to mention invalid configuration, got: %v", err)
	}
}

func TestForceBackfill(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{string(config.BackfillModeAuto), false},
		{string(config.BackfillModeOff), false},
		{string(config.BackfillModeForce), true},
	}

	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			if got := forceBackfill(&config.Config{BackfillMode: tc.mode}); got != tc.want {
				t.Errorf("expected %t, got %t", tc.want, got)
			}
		})
	}
}

func TestBackfillSinceField(t *testing.T) {
	if got := backfillSinceField(time.Time{}); got != "full history" {
		t.Errorf("expected an unbounded window to be named, got %q", got)
	}

	since := time.Date(2024, time.January, 15, 0, 0, 0, 0, time.UTC)
	if got := backfillSinceField(since); got != "2024-01-15T00:00:00Z" {
		t.Errorf("expected an RFC 3339 timestamp, got %q", got)
	}
}

func TestEnvFloatOrDefault(t *testing.T) {
	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_RATE", "")

	if got := envFloatOrDefault("GITLAB_ACHIEVEMENTS_TEST_RATE", 5); got != 5 {
		t.Errorf("expected the fallback, got %v", got)
	}

	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_RATE", "2.5")

	if got := envFloatOrDefault("GITLAB_ACHIEVEMENTS_TEST_RATE", 5); got != 2.5 {
		t.Errorf("expected the env value, got %v", got)
	}

	// An unreadable value defers to Validate, which names the flag.
	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_RATE", "fast")

	if got := envFloatOrDefault("GITLAB_ACHIEVEMENTS_TEST_RATE", 5); got != 5 {
		t.Errorf("expected the fallback for an unreadable value, got %v", got)
	}
}
