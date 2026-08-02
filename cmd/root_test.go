package cmd

import (
	"net/http"
	"net/http/httptest"
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
		"--public-url", "https://achievements.example.com",
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

// TestBuildRootCmd_SecretsAreNotFlagDefaults guards the property that keeps
// credentials out of the usage output: the environment seeds the config
// value, but never the flag's default, which is what --help renders.
func TestBuildRootCmd_SecretsAreNotFlagDefaults(t *testing.T) {
	secrets := map[string]string{
		"GITLAB_READ_TOKEN":   "read-token-from-env",
		"GITLAB_WRITE_TOKEN":  "write-token-from-env",
		"DATABASE_DSN":        "postgres://user:password-from-env@localhost/achievements",
		"WEBHOOK_SECRET":      "webhook-secret-from-env",
		"OAUTH_CLIENT_SECRET": "oauth-secret-from-env",
	}

	for key, value := range secrets {
		t.Setenv(key, value)
	}

	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	// The environment still configures the process.
	if cfg.GitLabWriteToken != secrets["GITLAB_WRITE_TOKEN"] {
		t.Errorf("expected the write token to come from the environment, got %q", cfg.GitLabWriteToken)
	}

	if cfg.DatabaseDSN != secrets["DATABASE_DSN"] {
		t.Errorf("expected the database DSN to come from the environment, got %q", cfg.DatabaseDSN)
	}

	usage := rootCmd.UsageString()

	for key, value := range secrets {
		if strings.Contains(usage, value) {
			t.Errorf("usage output leaks %s: %q", key, usage)
		}
	}
}

// TestBuildRootCmd_ExplicitSecretFlagWins checks that seeding the value
// after registration does not stop the flag from overriding it.
func TestBuildRootCmd_ExplicitSecretFlagWins(t *testing.T) {
	t.Setenv("GITLAB_WRITE_TOKEN", "from-env")

	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.ParseFlags([]string{"--gitlab-write-token", "from-flag"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.GitLabWriteToken != "from-flag" {
		t.Errorf("expected the flag to win over the environment, got %q", cfg.GitLabWriteToken)
	}
}

// TestBuildRootCmd_SilencesUsageOnRuntimeError pins the other half: a
// failure inside RunE must not make cobra print the flag list.
func TestBuildRootCmd_SilencesUsageOnRuntimeError(t *testing.T) {
	rootCmd := buildRootCmd(&config.Config{})

	if !rootCmd.SilenceUsage {
		t.Error("expected SilenceUsage so runtime failures do not print every flag's default")
	}

	if !rootCmd.SilenceErrors {
		t.Error("expected SilenceErrors so Execute is the only thing printing the error")
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

func TestRun_BootstrapFailure(t *testing.T) {
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer gitlabServer.Close()

	cfg := &config.Config{
		GitLabURL:             gitlabServer.URL,
		GitLabReadToken:       "read-token",
		GitLabWriteToken:      "write-token",
		AchievementsNamespace: "achievements",
		DatabaseDSN:           "sqlite://:memory:",
		WebhookSecret:         "s3cr3t",
		PublicURL:             "https://achievements.example.com",
	}

	err := run(cfg)
	if err == nil {
		t.Fatal("expected an error for a GitLab instance rejecting both tokens, got nil")
	}

	if !strings.Contains(err.Error(), "bootstrap failed") {
		t.Errorf("expected error to mention bootstrap failure, got: %v", err)
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
