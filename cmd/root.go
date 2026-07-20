// Package cmd wires the command-line interface for gitlab-achievements.
package cmd

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/logging"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

const shortHashLength = 7

// version and buildTime can optionally be set at build time via -ldflags.
// When they are unset (for example with `go install`), version resolution
// falls back to runtime/debug.ReadBuildInfo.
//
//nolint:gochecknoglobals // intentional ldflags injection targets
var (
	version   string // set via -X github.com/boxboxjason/gitlab-achievements/cmd.version=vX.Y.Z
	buildTime string // set via -X github.com/boxboxjason/gitlab-achievements/cmd.buildTime=2006-01-02T15:04:05Z
)

// Execute runs the CLI command.
func Execute() {
	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}

func buildRootCmd(cfg *config.Config) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "gitlab-achievements",
		Version: versionInfo(),
		Short:   "Event-driven achievement awarder for self-hosted GitLab instances",
		Long:    "Watches a self-hosted GitLab instance's activity and automatically awards GitLab Achievements to users.",
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(cfg)
		},
	}

	bindFlags(rootCmd, cfg)

	return rootCmd
}

func bindFlags(rootCmd *cobra.Command, cfg *config.Config) {
	rootCmd.Flags().StringVar(&cfg.GitLabURL, "gitlab-url", os.Getenv("GITLAB_URL"), "Base URL of the GitLab instance")
	rootCmd.Flags().StringVar(&cfg.GitLabReadToken, "gitlab-read-token", os.Getenv("GITLAB_READ_TOKEN"), "Read-only GitLab token (read_api scope)")
	rootCmd.Flags().StringVar(&cfg.GitLabWriteToken, "gitlab-write-token", os.Getenv("GITLAB_WRITE_TOKEN"), "Write-capable GitLab token (api scope), scoped down by role")
	rootCmd.Flags().StringVar(&cfg.AchievementsNamespace, "achievements-namespace", os.Getenv("ACHIEVEMENTS_NAMESPACE"), "Full path of the namespace that owns the achievement definitions")
	rootCmd.Flags().StringVar(&cfg.DatabaseDSN, "database-dsn", os.Getenv("DATABASE_DSN"), "PostgreSQL connection string")
	rootCmd.Flags().StringVar(&cfg.WebhookSecret, "webhook-secret", os.Getenv("WEBHOOK_SECRET"), "Secret token used to validate incoming GitLab webhook deliveries")
	rootCmd.Flags().StringVar(&cfg.ListenAddr, "listen-addr", envOrDefault("LISTEN_ADDR", config.DefaultListenAddr), "Address the HTTP server listens on")
	rootCmd.Flags().StringVar(&cfg.LogLevel, "log-level", envOrDefault("LOG_LEVEL", config.DefaultLogLevel), "Log level (debug, info, warn, error)")
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

func run(cfg *config.Config) error {
	err := cfg.Validate()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	logger, err := logging.Setup(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("failed to set up logger: %w", err)
	}

	defer func() { _ = logger.Sync() }()

	logger.Info("configuration loaded",
		zap.String("gitlab_url", cfg.GitLabURL),
		zap.String("achievements_namespace", cfg.AchievementsNamespace),
		zap.String("listen_addr", cfg.ListenAddr),
	)
	logger.Warn("gitlab-achievements does not implement bootstrap, backfill, or event ingestion yet: this is scaffolding only")

	return nil
}

// versionInfo returns a human-readable version string of the form:
//
//	v1.2.3 (go: go1.26.5, built: 2026-02-22T20:00:00Z)
//
// Version resolution order:
//  1. ldflags-injected version  (make build / make build version=x.y.z)
//  2. Module version from debug.ReadBuildInfo  (go install @vX.Y.Z)
//  3. VCS commit hash from build settings  (local go build / go install @latest)
//  4. "dev" as final fallback
//
// Build time resolution order:
//  1. ldflags-injected buildTime  (make build)
//  2. vcs.time from build settings
//  3. "unknown"
func versionInfo() string {
	return fmt.Sprintf("%s (go: %s, built: %s)", resolveVersion(), runtime.Version(), resolveBuildTime())
}

// resolveVersion returns the most specific version string available.
func resolveVersion() string {
	// ldflags injection wins — used by `make build` and CI releases.
	if version != "" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}

	// `go install @vX.Y.Z` populates Main.Version with the module tag.
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	// Local `go build` / `go install @latest` — fall back to VCS revision.
	return vcsRevision(info)
}

// vcsRevision extracts the short commit hash (and a "-dirty" suffix when the
// working tree has uncommitted changes) from the VCS build settings.
func vcsRevision(info *debug.BuildInfo) string {
	var (
		revision string
		dirty    bool
	)

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if len(setting.Value) > shortHashLength {
				revision = setting.Value[:shortHashLength]
			} else {
				revision = setting.Value
			}
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}

	if revision == "" {
		return "dev"
	}

	if dirty {
		return revision + "-dirty"
	}

	return revision
}

// resolveBuildTime returns the build timestamp, falling back through ldflags →
// vcs.time build setting → "unknown".
func resolveBuildTime() string {
	if buildTime != "" {
		return buildTime
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.time" {
			return setting.Value
		}
	}

	return "unknown"
}
