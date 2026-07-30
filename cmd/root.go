// Package cmd wires the command-line interface for gitlab-achievements.
package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/bootstrap"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/engine"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
	"github.com/boxboxjason/gitlab-achievements/internal/httpserver"
	"github.com/boxboxjason/gitlab-achievements/internal/logging"
	"github.com/boxboxjason/gitlab-achievements/internal/scheduler"
	"github.com/boxboxjason/gitlab-achievements/internal/webhook"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

const (
	// shutdownTimeout bounds how long graceful shutdown waits for
	// in-flight requests to finish once a termination signal is received.
	shutdownTimeout = 10 * time.Second
	// readHeaderTimeout bounds how long the server waits to read request
	// headers, mitigating slow-loris style connections.
	readHeaderTimeout = 5 * time.Second
	// webhookReconcileInterval sets how often the event ingestion hooks are
	// re-checked, repaired if they were altered or deleted, and registered
	// on groups and projects created since the last sweep.
	//
	// The sweep touches every group (or every project) on the instance, at
	// roughly one API call per target, so it is paced hourly: running it
	// often enough to notice a deleted hook within minutes would mean a
	// permanent background load on someone's production GitLab.
	webhookReconcileInterval = time.Hour
	// achievementReconcileInterval sets how often achievement existence and
	// award status are re-checked and repaired after bootstrap.
	achievementReconcileInterval = time.Hour
	// webhookShutdownTimeout bounds how long shutdown waits for activity
	// already accepted from GitLab to be evaluated before giving up on it.
	webhookShutdownTimeout = 15 * time.Second
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
	rootCmd.AddCommand(buildBackfillCmd(cfg))

	return rootCmd
}

// bindFlags registers the configuration flags as persistent ones so that
// subcommands (which run the same startup sequence against the same
// instance) are configured identically to the server, from one set of flags
// and environment variables.
func bindFlags(rootCmd *cobra.Command, cfg *config.Config) {
	flags := rootCmd.PersistentFlags()

	flags.StringVar(&cfg.GitLabURL, "gitlab-url", os.Getenv("GITLAB_URL"), "Base URL of the GitLab instance")
	flags.StringVar(&cfg.GitLabReadToken, "gitlab-read-token", os.Getenv("GITLAB_READ_TOKEN"), "Read-only GitLab token (read_api scope)")
	flags.StringVar(&cfg.GitLabWriteToken, "gitlab-write-token", os.Getenv("GITLAB_WRITE_TOKEN"), "Write-capable GitLab token (api scope), scoped down by role")
	flags.StringVar(&cfg.AchievementsNamespace, "achievements-namespace", os.Getenv("ACHIEVEMENTS_NAMESPACE"), "Full path of the namespace that owns the achievement definitions")
	flags.StringVar(&cfg.DatabaseDSN, "database-dsn", os.Getenv("DATABASE_DSN"), "Database connection string (postgres://, sqlite://, mysql://, or sqlserver://)")
	flags.StringVar(&cfg.WebhookSecret, "webhook-secret", os.Getenv("WEBHOOK_SECRET"), "Secret token used to validate incoming GitLab webhook deliveries")
	flags.StringVar(&cfg.PublicURL, "public-url", os.Getenv("PUBLIC_URL"), "Externally reachable base URL of this app, used to register its GitLab webhooks")
	flags.StringVar(&cfg.ListenAddr, "listen-addr", envOrDefault("LISTEN_ADDR", config.DefaultListenAddr), "Address the HTTP server listens on")
	flags.StringVar(&cfg.LogLevel, "log-level", envOrDefault("LOG_LEVEL", config.DefaultLogLevel), "Log level (debug, info, warn, error)")
	flags.StringVar(&cfg.HookScope, "hook-scope", envOrDefault("HOOK_SCOPE", string(config.DefaultHookScope)), "Which webhooks to register for event ingestion (auto, group, project); auto picks group hooks where the license allows them")
	flags.StringVar(&cfg.BackfillMode, "backfill", envOrDefault("BACKFILL", string(config.DefaultBackfillMode)), "Whether the server walks the instance's history itself (auto, off, force)")
	flags.StringVar(&cfg.BackfillSince, "backfill-since", os.Getenv("BACKFILL_SINCE"), "How far back the historical backfill reaches, as a date (2006-01-02) or duration (720h); empty walks everything")
	flags.Float64Var(&cfg.BackfillRate, "backfill-rate", envFloatOrDefault("BACKFILL_RATE", config.DefaultBackfillRate), "Requests per second the historical backfill is allowed to issue")
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

// envFloatOrDefault reads a numeric environment variable, falling back to
// fallback when it is unset or unparseable. An unparseable value is not an
// error here: Validate reports the resulting configuration problem with the
// flag's name attached, which is more use to an operator than a parse error
// naming only the variable.
func envFloatOrDefault(key string, fallback float64) float64 {
	parsed, err := strconv.ParseFloat(os.Getenv(key), 64)
	if err != nil {
		return fallback
	}

	return parsed
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

	defer logger.Sync() //nolint:errcheck // ignore sync errors on exit

	logger.Info("configuration loaded",
		zap.String("gitlab_url", cfg.GitLabURL),
		zap.String("achievements_namespace", cfg.AchievementsNamespace),
		zap.String("listen_addr", cfg.ListenAddr),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, sqlDB, err := openDatabase(cfg)
	if err != nil {
		return err
	}

	defer sqlDB.Close() //nolint:errcheck // ignore close errors on exit

	logger.Info("database connected and migrated")

	webhookURL := cfg.PublicURL + httpserver.WebhookPath

	// Captured before bootstrap registers the hooks, since that is the
	// moment GitLab may start delivering events: it becomes the ceiling on
	// what the historical walk counts, so the two paths never both count
	// the same activity. See backfill.Options.Until.
	liveFrom := time.Now()

	readClient, writeClient, report, err := bootstrapApp(ctx, cfg, conn, webhookURL, logger)
	if err != nil {
		return err
	}

	return serve(ctx, cfg, conn, sqlDB, readClient, writeClient, report, webhookURL, liveFrom, logger)
}

// openDatabase connects to the database, verifies the connection, and
// brings the schema up to date.
func openDatabase(cfg *config.Config) (*gorm.DB, *sql.DB, error) {
	conn, err := db.Open(cfg.DatabaseDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to access underlying database connection: %w", err)
	}

	err = db.Migrate(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to migrate database schema: %w", err)
	}

	return conn, sqlDB, nil
}

// bootstrapApp builds the GitLab clients and runs bootstrap (permission
// verification, achievement/webhook reconciliation), returning the clients
// and bootstrap report for reuse by the readiness check and the periodic
// reconciliation loops.
func bootstrapApp(ctx context.Context, cfg *config.Config, conn *gorm.DB, webhookURL string, logger *zap.Logger) (*gitlabclient.ReadClient, *gitlabclient.WriteClient, *bootstrap.Report, error) {
	readClient, err := gitlabclient.NewReadClient(cfg.GitLabURL, cfg.GitLabReadToken)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to build gitlab read client: %w", err)
	}

	writeClient, err := gitlabclient.NewWriteClient(cfg.GitLabURL, cfg.GitLabWriteToken)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to build gitlab write client: %w", err)
	}

	report, err := bootstrap.Run(ctx, bootstrap.Client{Read: readClient, Write: writeClient}, conn, cfg, webhookURL, logger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bootstrap failed: %w", err)
	}

	logger.Info("bootstrap complete",
		zap.Int64("namespace_id", report.NamespaceID),
		zap.Int("achievements_created", report.Achievements.Created),
		zap.Int("achievements_updated", report.Achievements.Updated),
		zap.Int("achievements_unchanged", report.Achievements.Unchanged),
		zap.String("hook_scope", string(report.Webhook.Scope)),
		zap.Int("hook_targets", report.Webhook.Targets),
		zap.Int("hooks_created", report.Webhook.Created),
		zap.Int("hooks_updated", report.Webhook.Updated),
		zap.Int("hook_targets_skipped", report.Webhook.Skipped),
	)

	return readClient, writeClient, report, nil
}

// newHTTPServer builds the *http.Server exposing /healthz, /readyz, and the
// webhook ingestion endpoint, wired to check the database connection and
// GitLab reachability on every /readyz request.
func newHTTPServer(cfg *config.Config, sqlDB *sql.DB, readClient *gitlabclient.ReadClient, receiver http.Handler, logger *zap.Logger) *http.Server {
	srv := httpserver.New(
		func(ctx context.Context) error {
			return sqlDB.PingContext(ctx)
		},
		func(ctx context.Context) error {
			_, err := readClient.CurrentUser(gitlab.WithContext(ctx))
			if err != nil {
				return fmt.Errorf("gitlab readiness check failed: %w", err)
			}

			return nil
		},
		func(reason string, err error) {
			logger.Warn(reason, zap.Error(err))
		},
	)
	srv.MountWebhook(receiver)
	srv.SetReady(true)

	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
}

// serve starts the HTTP server exposing /healthz, /readyz, and webhook
// ingestion, along with the background reconciliation loops that keep
// GitLab-side state healed after bootstrap. It blocks until SIGINT/SIGTERM
// is received, then shuts down gracefully.
func serve(
	ctx context.Context,
	cfg *config.Config,
	conn *gorm.DB,
	sqlDB *sql.DB,
	readClient *gitlabclient.ReadClient,
	writeClient *gitlabclient.WriteClient,
	report *bootstrap.Report,
	webhookURL string,
	liveFrom time.Time,
	logger *zap.Logger,
) error {
	// The queue and the engine behind it are what live deliveries are
	// evaluated by; the backfill builds its own engine, since the two count
	// different windows of the same instance's activity.
	queue := webhook.NewQueue(engine.New(conn), webhook.Options{Logger: logger})
	queue.Start(ctx)

	receiver := webhook.NewReceiver(cfg.WebhookSecret, queue, logger)
	httpSrv := newHTTPServer(cfg, sqlDB, readClient, receiver, logger)

	startReconciliationLoops(ctx, cfg, conn, readClient, writeClient, report.NamespaceID, webhookURL, logger)
	startBackfill(ctx, cfg, conn, writeClient, liveFrom, logger)

	serveErr := make(chan error, 1)

	go func() {
		logger.Info("listening", zap.String("addr", cfg.ListenAddr))

		listenErr := httpSrv.ListenAndServe()
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			serveErr <- listenErr

			return
		}

		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server failed: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutting down")

		// Deliberately rooted in context.Background(), not ctx: ctx is
		// already Done() here (that's why we're in this branch), so
		// deriving from it would hand Shutdown an already-expired
		// context instead of the fresh shutdownTimeout window it needs.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		shutdownErr := httpSrv.Shutdown(shutdownCtx) //nolint:contextcheck // see comment above
		if shutdownErr != nil {
			return fmt.Errorf("failed to shut down http server gracefully: %w", shutdownErr)
		}

		drainQueue(queue, logger) //nolint:contextcheck // drains on a fresh context, see below
	}

	return nil
}

// drainQueue gives activity already accepted from GitLab a chance to be
// evaluated before the process exits.
//
// It runs after the HTTP server has stopped, so nothing new is arriving,
// and on a context of its own for the same reason the server's shutdown
// does: the one that triggered shutdown is already cancelled. Activity
// still queued when the deadline passes is reported rather than dropped
// silently, since GitLab has already been told those deliveries succeeded
// and will not send them again.
func drainQueue(queue *webhook.Queue, logger *zap.Logger) {
	drainCtx, cancel := context.WithTimeout(context.Background(), webhookShutdownTimeout)
	defer cancel()

	err := queue.Shutdown(drainCtx)
	stats := queue.Stats()

	if err != nil {
		logger.Error("gave up draining accepted webhook activity, it was not evaluated",
			zap.Int("pending", stats.Pending),
			zap.Error(err),
		)

		return
	}

	logger.Info("webhook activity drained",
		zap.Int64("accepted", stats.Accepted),
		zap.Int64("processed", stats.Processed),
		zap.Int64("failed", stats.Failed),
		zap.Int64("rejected", stats.Rejected),
	)
}

// startReconciliationLoops launches the background jobs that keep GitLab-side
// state healed after bootstrap's one-time check: a hook deleted or altered
// on GitLab's side, a group or project created since the last sweep, an
// achievement deleted, or an award GitLab didn't confirm is repaired on its
// own cadence instead of only at the next process restart. Both loops stop
// when ctx is cancelled.
func startReconciliationLoops(
	ctx context.Context,
	cfg *config.Config,
	conn *gorm.DB,
	readClient *gitlabclient.ReadClient,
	writeClient *gitlabclient.WriteClient,
	namespaceID int64,
	webhookURL string,
	logger *zap.Logger,
) {
	go scheduler.Every(ctx, webhookReconcileInterval, func(ctx context.Context) error {
		hooks, err := bootstrap.ReconcileWebhooks(ctx, readClient, writeClient, conn, cfg, webhookURL, logger)
		if err != nil {
			return fmt.Errorf("webhook reconciliation failed: %w", err)
		}

		logger.Info("webhook reconciliation complete",
			zap.String("hook_scope", string(hooks.Scope)),
			zap.Int("hook_targets", hooks.Targets),
			zap.Int("hooks_created", hooks.Created),
			zap.Int("hook_targets_skipped", hooks.Skipped),
		)

		return nil
	}, func(err error) {
		logger.Error("webhook reconciliation failed", zap.Error(err))
	})

	go scheduler.Every(ctx, achievementReconcileInterval, func(ctx context.Context) error {
		hourly, err := bootstrap.RunHourlyReconciliation(ctx, writeClient, conn, namespaceID, cfg.AchievementsNamespace)
		if err != nil {
			return fmt.Errorf("achievement reconciliation failed: %w", err)
		}

		logger.Info("achievement reconciliation complete",
			zap.Int("achievements_recreated", hourly.Achievements.Recreated),
			zap.Int("achievements_unchanged", hourly.Achievements.Unchanged),
			zap.Int("awards_confirmed", hourly.Awards.Confirmed),
			zap.Int("awards_failed", hourly.Awards.Failed),
		)

		return nil
	}, func(err error) {
		logger.Error("achievement reconciliation failed", zap.Error(err))
	})
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
	// ldflags injection wins, used by `make build` and CI releases.
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

	// Local `go build` / `go install @latest`, fall back to VCS revision.
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
