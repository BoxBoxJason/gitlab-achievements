package cmd

import (
	"strings"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
)

func TestBuildRootCmd_FlagsBindToConfig(t *testing.T) {
	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.ParseFlags([]string{
		"--gitlab-url", "https://gitlab.example.com",
		"--gitlab-read-token", "read-token",
		"--gitlab-write-token", "write-token",
		"--achievements-namespace", "achievements",
		"--database-dsn", "postgres://localhost/achievements",
		"--webhook-secret", "s3cr3t",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.GitLabURL != "https://gitlab.example.com" {
		t.Errorf("expected gitlab-url to be bound, got %q", cfg.GitLabURL)
	}

	if cfg.ListenAddr != config.DefaultListenAddr {
		t.Errorf("expected default listen addr, got %q", cfg.ListenAddr)
	}
}

func TestRun_InvalidConfig(t *testing.T) {
	err := run(&config.Config{})
	if err == nil {
		t.Fatal("expected an error for an empty config, got nil")
	}

	if !strings.Contains(err.Error(), "invalid configuration") {
		t.Errorf("expected error to mention invalid configuration, got: %v", err)
	}
}

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_VAR", "")

	if got := envOrDefault("GITLAB_ACHIEVEMENTS_TEST_VAR", "fallback"); got != "fallback" {
		t.Errorf("expected fallback value, got %q", got)
	}

	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_VAR", "set-value")

	if got := envOrDefault("GITLAB_ACHIEVEMENTS_TEST_VAR", "fallback"); got != "set-value" {
		t.Errorf("expected env value, got %q", got)
	}
}

func TestVersionInfo(t *testing.T) {
	info := versionInfo()
	if info == "" {
		t.Fatal("expected a non-empty version string")
	}

	if !strings.Contains(info, "go:") {
		t.Errorf("expected version info to mention the go runtime version, got: %q", info)
	}
}
