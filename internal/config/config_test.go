package config

import (
	"strings"
	"testing"
	"time"
)

func validConfig() *Config {
	return &Config{
		GitLabURL:             "https://gitlab.example.com",
		GitLabReadToken:       "read-token",
		GitLabWriteToken:      "write-token",
		AchievementsNamespace: "achievements",
		DatabaseDSN:           "postgres://user:pass@localhost:5432/achievements",
		WebhookSecret:         "s3cr3t",
		PublicURL:             "https://achievements.example.com",
	}
}

func TestValidate_Valid(t *testing.T) {
	cfg := validConfig()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("expected default listen addr %q, got %q", DefaultListenAddr, cfg.ListenAddr)
	}

	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("expected default log level %q, got %q", DefaultLogLevel, cfg.LogLevel)
	}
}

func TestValidate_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"missing gitlab-url", func(c *Config) { c.GitLabURL = "" }, "gitlab-url is required"},
		{"missing gitlab-read-token", func(c *Config) { c.GitLabReadToken = "" }, "gitlab-read-token is required"},
		{"missing gitlab-write-token", func(c *Config) { c.GitLabWriteToken = "" }, "gitlab-write-token is required"},
		{"missing achievements-namespace", func(c *Config) { c.AchievementsNamespace = "" }, "achievements-namespace is required"},
		{"missing database-dsn", func(c *Config) { c.DatabaseDSN = "" }, "database-dsn is required"},
		{"missing webhook-secret", func(c *Config) { c.WebhookSecret = "" }, "webhook-secret is required"},
		{"missing public-url", func(c *Config) { c.PublicURL = "" }, "public-url is required"},
		{"blank gitlab-url (whitespace only)", func(c *Config) { c.GitLabURL = "   " }, "gitlab-url is required"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error to contain %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_InvalidURL(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"invalid gitlab-url", func(c *Config) { c.GitLabURL = "not-a-url" }, `gitlab-url is not a valid absolute URL: "not-a-url"`},
		{"invalid public-url", func(c *Config) { c.PublicURL = "not-a-url" }, `public-url is not a valid absolute URL: "not-a-url"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error to contain %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_StripsTrailingSlashFromPublicURL(t *testing.T) {
	cfg := validConfig()
	cfg.PublicURL = "https://achievements.example.com///"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.PublicURL != "https://achievements.example.com" {
		t.Errorf("expected trailing slashes to be stripped, got %q", cfg.PublicURL)
	}
}

func TestValidate_SameReadAndWriteToken(t *testing.T) {
	cfg := validConfig()
	cfg.GitLabWriteToken = cfg.GitLabReadToken

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error when read and write tokens are identical, got nil")
	}

	if !strings.Contains(err.Error(), "must not be the same credential") {
		t.Errorf("expected error to mention identical credentials, got: %v", err)
	}
}

func TestValidate_AggregatesAllErrors(t *testing.T) {
	cfg := &Config{}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for an empty config, got nil")
	}

	for _, f := range cfg.requiredFields() {
		if !strings.Contains(err.Error(), f.name+" is required") {
			t.Errorf("expected aggregated error to mention %q, got: %v", f.name, err)
		}
	}
}

func TestValidate_AppliesBackfillDefaults(t *testing.T) {
	cfg := validConfig()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.BackfillMode != string(DefaultBackfillMode) {
		t.Errorf("expected default backfill mode %q, got %q", DefaultBackfillMode, cfg.BackfillMode)
	}

	if cfg.BackfillRate != DefaultBackfillRate {
		t.Errorf("expected default backfill rate %v, got %v", DefaultBackfillRate, cfg.BackfillRate)
	}
}

func TestValidate_BackfillMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{"auto", string(BackfillModeAuto), false},
		{"off", string(BackfillModeOff), false},
		{"force", string(BackfillModeForce), false},
		{"unknown", "sometimes", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.BackfillMode = tc.mode

			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}

			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			if tc.wantErr && !strings.Contains(err.Error(), "backfill must be one of") {
				t.Errorf("expected error to name the accepted modes, got: %v", err)
			}
		})
	}
}

func TestValidate_BackfillRateMustBePositive(t *testing.T) {
	cfg := validConfig()
	cfg.BackfillRate = -1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for a non-positive rate, got nil")
	}

	if !strings.Contains(err.Error(), "backfill-rate must be greater than 0") {
		t.Errorf("expected error to mention the rate, got: %v", err)
	}
}

func TestParseBackfillSince(t *testing.T) {
	tests := []struct {
		name    string
		since   string
		wantErr bool
		check   func(*testing.T, time.Time)
	}{
		{
			name:  "empty walks the full history",
			since: "",
			check: func(t *testing.T, got time.Time) {
				t.Helper()

				if !got.IsZero() {
					t.Errorf("expected the zero time, got %s", got)
				}
			},
		},
		{
			name:  "calendar date",
			since: "2024-01-15",
			check: func(t *testing.T, got time.Time) {
				t.Helper()

				want := time.Date(2024, time.January, 15, 0, 0, 0, 0, time.UTC)
				if !got.Equal(want) {
					t.Errorf("expected %s, got %s", want, got)
				}
			},
		},
		{
			name:  "duration counted back from now",
			since: "24h",
			check: func(t *testing.T, got time.Time) {
				t.Helper()

				elapsed := time.Since(got)
				if elapsed < 23*time.Hour || elapsed > 25*time.Hour {
					t.Errorf("expected roughly 24h ago, got %s", got)
				}
			},
		},
		{"garbage", "last tuesday", true, nil},
		{"negative duration", "-24h", true, nil},
		{"zero duration", "0s", true, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.BackfillSince = tc.since

			got, err := cfg.ParseBackfillSince()

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}

				return
			}

			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			tc.check(t, got)
		})
	}
}

func TestValidate_ReportsAnUnreadableBackfillWindow(t *testing.T) {
	cfg := validConfig()
	cfg.BackfillSince = "last tuesday"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if !strings.Contains(err.Error(), "backfill-since must be a date") {
		t.Errorf("expected error to explain the accepted formats, got: %v", err)
	}
}

func TestValidate_RejectsAnUnknownHookScope(t *testing.T) {
	cfg := validConfig()
	cfg.HookScope = "everything"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an unknown hook scope to be rejected")
	}

	if !strings.Contains(err.Error(), "hook-scope") {
		t.Errorf("expected the error to name the flag, got: %v", err)
	}
}

func TestValidate_AcceptsEveryHookScope(t *testing.T) {
	for _, scope := range []HookScope{HookScopeAuto, HookScopeGroup, HookScopeProject} {
		cfg := validConfig()
		cfg.HookScope = string(scope)

		if err := cfg.Validate(); err != nil {
			t.Errorf("scope %q: expected no error, got: %v", scope, err)
		}
	}
}

func TestValidate_DefaultsHookScopeToAuto(t *testing.T) {
	cfg := validConfig()
	cfg.HookScope = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.HookScope != string(HookScopeAuto) {
		t.Errorf("expected the scope to default to resolving from the license, got %q", cfg.HookScope)
	}
}
