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

	"github.com/boxboxjason/gitlab-achievements/internal/api"
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
	rootCmd.AddCommand(buildReconcileCmd(cfg))
	rootCmd.AddCommand(buildUninstallCmd(cfg))

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
	flags.Float64Var(&cfg.HookRate, "hook-rate", envFloatOrDefault("HOOK_RATE", config.DefaultHookRate), "Groups or projects per second the webhook registration sweep works through")
	flags.StringVar(&cfg.BackfillMode, "backfill", envOrDefault("BACKFILL", string(config.DefaultBackfillMode)), "Whether the server walks the instance's history itself (auto, off, force)")
	flags.StringVar(&cfg.BackfillSince, "backfill-since", os.Getenv("BACKFILL_SINCE"), "How far back the historical backfill reaches, as a date (2006-01-02) or duration (720h); empty walks everything")
	flags.Float64Var(&cfg.BackfillRate, "backfill-rate", envFloatOrDefault("BACKFILL_RATE", config.DefaultBackfillRate), "Requests per second the historical backfill is allowed to issue")
	flags.StringVar(&cfg.ReconcileMode, "reconcile", envOrDefault("RECONCILE", string(config.DefaultReconcileMode)), "Whether the server re-reads recent activity on a timer to heal lost webhook deliveries (auto, off)")
	flags.DurationVar(&cfg.ReconcileInterval, "reconcile-interval", envDurationOrDefault("RECONCILE_INTERVAL", config.DefaultReconcileInterval), "How often the reconciliation sync re-reads recent activity")
	flags.DurationVar(&cfg.ReconcileLookback, "reconcile-lookback", envDurationOrDefault("RECONCILE_LOOKBACK", config.DefaultReconcileLookback), "How far back each reconciliation pass reaches; must exceed --reconcile-interval so passes overlap")
	flags.StringVar(&cfg.APIAuth, "api-auth", envOrDefault("API_AUTH", string(config.DefaultAPIAuth)), "What the read API requires of callers (none, gitlab); gitlab verifies a GitLab token on every request")
	flags.StringVar(&cfg.OAuthClientID, "oauth-client-id", os.Getenv("OAUTH_CLIENT_ID"), "Client ID of a hand-registered GitLab OAuth application; empty lets the app register a public one for itself")
	flags.StringVar(&cfg.OAuthClientSecret, "oauth-client-secret", os.Getenv("OAUTH_CLIENT_SECRET"), "Client secret for --oauth-client-id, making it a confidential client; empty means PKCE alone")
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

// envDurationOrDefault reads a Go duration from an environment variable,
// falling back for the same reason envFloatOrDefault does: Validate reports
// the resulting problem against the flag's name, which is more use to an
// operator than a parse error naming only the variable.
func envDurationOrDefault(key string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(os.Getenv(key))
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

	zap.L().Info("configuration loaded",
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

	zap.L().Info("database connected and migrated")

	webhookURL := cfg.PublicURL + httpserver.WebhookPath

	// Captured before bootstrap registers any hooks, and used as the
	// ceiling on what the historical walk counts. See backfill.Options.Until
	// for why the two paths need disjoint windows at all.
	//
	// Each hook starts delivering when the sweep reaches it, so this is
	// earlier than live ingestion actually begins, and activity in between
	// is counted by neither path. The alternative is a ceiling later than
	// some hook's registration, which double-counts instead, and a single
	// ceiling cannot avoid both: a gap is recoverable by the planned
	// activity reconciliation, whereas an inflated counter is permanent.
	// The gap's width is the sweep's duration, logged below.
	liveFrom := time.Now()

	readClient, writeClient, report, err := bootstrapApp(ctx, cfg, conn, webhookURL)
	if err != nil {
		return err
	}

	zap.L().Info("live ingestion active",
		zap.Duration("uncovered_window", time.Since(liveFrom)),
		zap.Time("history_walked_until", liveFrom),
	)

	return serve(ctx, cfg, conn, sqlDB, readClient, writeClient, report, webhookURL, liveFrom)
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
func bootstrapApp(ctx context.Context, cfg *config.Config, conn *gorm.DB, webhookURL string) (*gitlabclient.ReadClient, *gitlabclient.WriteClient, *bootstrap.Report, error) {
	readClient, err := gitlabclient.NewReadClient(cfg.GitLabURL, cfg.GitLabReadToken)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to build gitlab read client: %w", err)
	}

	writeClient, err := gitlabclient.NewWriteClient(cfg.GitLabURL, cfg.GitLabWriteToken)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to build gitlab write client: %w", err)
	}

	report, err := bootstrap.Run(ctx, bootstrap.Client{Read: readClient, Write: writeClient}, conn, cfg, webhookURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bootstrap failed: %w", err)
	}

	zap.L().Info("bootstrap complete",
		zap.Int64("namespace_id", report.NamespaceID),
		zap.Int("achievements_created", report.Achievements.Created),
		zap.Int("achievements_updated", report.Achievements.Updated),
		zap.Int("achievements_unchanged", report.Achievements.Unchanged),
		zap.Int("exp_totals_corrected", report.ExpTotalsCorrected),
		zap.String("hook_scope", string(report.Webhook.Scope)),
		zap.Int("hook_targets", report.Webhook.Targets),
		zap.Int("hooks_created", report.Webhook.Created),
		zap.Int("hooks_updated", report.Webhook.Updated),
		zap.Int("hook_targets_skipped", report.Webhook.Skipped),
	)

	return readClient, writeClient, report, nil
}

// buildAPI constructs the read API serving user EXP, achievement progress,
// and the leaderboard.
//
// With authentication configured, this is also where the OAuth application
// visitors log in through is resolved: an operator-registered one when its
// client ID is configured, otherwise one this app registers for itself and
// then adopts on every later start. Resolving it here, before the listener
// opens, means a misconfigured or unregisterable application fails startup
// rather than surfacing at somebody's first login attempt.
func buildAPI(ctx context.Context, cfg *config.Config, conn *gorm.DB, writeClient *gitlabclient.WriteClient) (*api.API, error) {
	opts := api.Options{}

	if cfg.AuthMode() != config.APIAuthGitLab {
		zap.L().Info("read api is unauthenticated", zap.String("api_auth", cfg.APIAuth))

		return api.New(conn, opts), nil
	}

	verifier, err := gitlabclient.NewTokenVerifier(cfg.GitLabURL)
	if err != nil {
		return nil, fmt.Errorf("failed to build gitlab token verifier: %w", err)
	}

	opts.Verifier = verifier

	clientID := cfg.OAuthClientID
	if clientID == "" {
		clientID, err = bootstrap.EnsureOAuthApplication(ctx, writeClient, conn, cfg.PublicURL+api.CallbackPath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve the oauth application for the read api: %w", err)
		}
	}

	opts.OAuth = &api.OAuthOptions{
		GitLabURL:    cfg.GitLabURL,
		PublicURL:    cfg.PublicURL,
		ClientID:     clientID,
		ClientSecret: cfg.OAuthClientSecret,
	}

	zap.L().Info("read api requires a gitlab credential",
		zap.String("oauth_client_id", clientID),
		zap.Bool("confidential_client", cfg.OAuthClientSecret != ""),
	)

	return api.New(conn, opts), nil
}

// buildServer assembles everything the listener serves: webhook ingestion,
// the read API, and the background sweep that clears expired sessions.
//
// It is separate from serve so that the pieces that can fail to build (the
// read API's OAuth application, in particular) are resolved before the
// listener opens, and so serve itself stays about the lifecycle rather than
// the construction.
func buildServer(
	ctx context.Context,
	cfg *config.Config,
	conn *gorm.DB,
	sqlDB *sql.DB,
	readClient *gitlabclient.ReadClient,
	writeClient *gitlabclient.WriteClient,
	queue *webhook.Queue,
) (*http.Server, error) {
	readAPI, err := buildAPI(ctx, cfg, conn, writeClient)
	if err != nil {
		return nil, err
	}

	startSessionPruning(ctx, cfg, readAPI)

	receiver := webhook.NewReceiver(cfg.WebhookSecret, queue)

	return newHTTPServer(cfg, sqlDB, readClient, receiver, readAPI), nil
}

// newHTTPServer builds the *http.Server exposing /healthz, /readyz, the
// webhook ingestion endpoint, and the read API, wired to check the database
// connection and GitLab reachability on every /readyz request.
func newHTTPServer(cfg *config.Config, sqlDB *sql.DB, readClient *gitlabclient.ReadClient, receiver http.Handler, readAPI *api.API) *http.Server {
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
			zap.L().Warn(reason, zap.Error(err))
		},
	)
	srv.MountWebhook(receiver)

	// Both prefixes reach the same handler, which dispatches within them:
	// the read endpoints sit behind whatever authentication is configured,
	// and the login routes deliberately do not, being how a browser
	// acquires a credential in the first place.
	srv.Mount(api.PathPrefix, readAPI.Handler())
	srv.Mount(api.OAuthPathPrefix, readAPI.Handler())

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
) error {
	// The queue and the engine behind it are what live deliveries are
	// evaluated by; the backfill builds its own engine, since the two count
	// different windows of the same instance's activity.
	queue := webhook.NewQueue(engine.New(conn), webhook.Options{})
	queue.Start(ctx)

	httpSrv, err := buildServer(ctx, cfg, conn, sqlDB, readClient, writeClient, queue)
	if err != nil {
		return err
	}

	startReconciliationLoops(ctx, cfg, conn, readClient, writeClient, report.NamespaceID, webhookURL)
	startBackfill(ctx, cfg, conn, writeClient, liveFrom)
	startReconcileLoop(ctx, cfg, conn, writeClient)

	serveErr := make(chan error, 1)

	go func() {
		zap.L().Info("listening", zap.String("addr", cfg.ListenAddr))

		listenErr := httpSrv.ListenAndServe()
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			serveErr <- listenErr

			return
		}

		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		// The listener is already gone, so nothing further can arrive; what
		// it accepted before failing still deserves to be evaluated.
		drainQueue(queue) //nolint:contextcheck // drains on a fresh context, see drainQueue

		if err != nil {
			return fmt.Errorf("http server failed: %w", err)
		}
	case <-ctx.Done():
		zap.L().Info("shutting down")

		// Deliberately rooted in context.Background(), not ctx: ctx is
		// already Done() here (that's why we're in this branch), so
		// deriving from it would hand Shutdown an already-expired
		// context instead of the fresh shutdownTimeout window it needs.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		shutdownErr := httpSrv.Shutdown(shutdownCtx) //nolint:contextcheck // see comment above

		// Drained before the error is returned, and regardless of it: a
		// server that shut down untidily has still accepted deliveries
		// GitLab was told succeeded.
		drainQueue(queue) //nolint:contextcheck // drains on a fresh context, see below

		if shutdownErr != nil {
			return fmt.Errorf("failed to shut down http server gracefully: %w", shutdownErr)
		}
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
func drainQueue(queue *webhook.Queue) {
	drainCtx, cancel := context.WithTimeout(context.Background(), webhookShutdownTimeout)
	defer cancel()

	err := queue.Shutdown(drainCtx)
	stats := queue.Stats()

	if err != nil {
		zap.L().Error("gave up draining accepted webhook activity, it was not evaluated",
			zap.Int("pending", stats.Pending),
			zap.Error(err),
		)

		return
	}

	// pending is reported even on the success path: it should always be
	// zero here, and a non-zero value is the signature of the workers
	// having stopped before the drain rather than because of it.
	zap.L().Info("webhook activity drained",
		zap.Int("pending", stats.Pending),
		zap.Int64("accepted", stats.Accepted),
		zap.Int64("processed", stats.Processed),
		zap.Int64("failed", stats.Failed),
		zap.Int64("rejected", stats.Rejected),
	)
}

// startSessionPruning launches the sweep that clears expired browser
// sessions from the database, stopping when ctx is cancelled.
//
// It runs only where the login flow can create sessions at all: with
// authentication off nothing ever writes that table, and a periodic DELETE
// against it would be pure noise. Expired sessions are already refused on
// every request, so this is housekeeping rather than enforcement.
func startSessionPruning(ctx context.Context, cfg *config.Config, readAPI *api.API) {
	if cfg.AuthMode() != config.APIAuthGitLab {
		return
	}

	go scheduler.Every(ctx, api.PruneInterval, func(ctx context.Context) error {
		pruned, err := readAPI.PruneSessions(ctx)
		if err != nil {
			return fmt.Errorf("session pruning failed: %w", err)
		}

		if pruned > 0 {
			zap.L().Info("pruned expired api sessions", zap.Int64("sessions", pruned))
		}

		return nil
	}, func(err error) {
		zap.L().Error("session pruning failed", zap.Error(err))
	})
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
) {
	go scheduler.Every(ctx, webhookReconcileInterval, func(ctx context.Context) error {
		hooks, err := bootstrap.ReconcileWebhooks(ctx, readClient, writeClient, conn, cfg, webhookURL)
		if err != nil {
			return fmt.Errorf("webhook reconciliation failed: %w", err)
		}

		zap.L().Info("webhook reconciliation complete",
			zap.String("hook_scope", string(hooks.Scope)),
			zap.Int("hook_targets", hooks.Targets),
			zap.Int("hooks_created", hooks.Created),
			zap.Int("hook_targets_skipped", hooks.Skipped),
		)

		return nil
	}, func(err error) {
		zap.L().Error("webhook reconciliation failed", zap.Error(err))
	})

	go scheduler.Every(ctx, achievementReconcileInterval, func(ctx context.Context) error {
		hourly, err := bootstrap.RunHourlyReconciliation(ctx, writeClient, conn, namespaceID, cfg.AchievementsNamespace)
		if err != nil {
			return fmt.Errorf("achievement reconciliation failed: %w", err)
		}

		zap.L().Info("achievement reconciliation complete",
			zap.Int("achievements_recreated", hourly.Achievements.Recreated),
			zap.Int("achievements_unchanged", hourly.Achievements.Unchanged),
			zap.Int("exp_totals_corrected", hourly.ExpTotalsCorrected),
			zap.Int("awards_confirmed", hourly.Awards.Confirmed),
			zap.Int("awards_failed", hourly.Awards.Failed),
			zap.Int("awards_superseded", hourly.Awards.Superseded),
			zap.Int("awards_adopted", hourly.Awards.Adopted),
		)

		return nil
	}, func(err error) {
		zap.L().Error("achievement reconciliation failed", zap.Error(err))
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
