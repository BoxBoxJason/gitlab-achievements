package config

import (
	"strings"
	"testing"
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
