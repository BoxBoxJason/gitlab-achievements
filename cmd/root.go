// Package cmd wires the command-line interface for gitlab-achievements.
package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/api"
	"github.com/boxboxjason/gitlab-achievements/internal/bootstrap"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/engine"
	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
	"github.com/boxboxjason/gitlab-achievements/internal/frontend"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
	"github.com/boxboxjason/gitlab-achievements/internal/httpserver"
	"github.com/boxboxjason/gitlab-achievements/internal/logging"
	"github.com/boxboxjason/gitlab-achievements/internal/metrics"
	"github.com/boxboxjason/gitlab-achievements/internal/origin"
	"github.com/boxboxjason/gitlab-achievements/internal/scheduler"
	"github.com/boxboxjason/gitlab-achievements/internal/webhook"
	"github.com/boxboxjason/gitlab-achievements/web"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
)

const (
	// shutdownTimeout bounds how long graceful shutdown waits for
	// in-flight requests to finish once a termination signal is received.
	shutdownTimeout = 10 * time.Second
	// readHeaderTimeout bounds how long the server waits to read request
	// headers, mitigating slow-loris style connections.
	readHeaderTimeout = 5 * time.Second
	// readTimeout bounds how long the server waits for a whole request,
	// body included. ReadHeaderTimeout alone leaves the body unbounded, and
	// a webhook body may be up to 10 MiB, so a client dripping one a byte
	// at a time would otherwise hold its goroutine for as long as it liked.
	// It is generous for anything legitimate: GitLab sends a full delivery
	// in well under a second.
	readTimeout = 30 * time.Second
	// writeTimeout bounds how long a response may take to write, measured
	// from the end of the request headers. The slowest handler is the OAuth
	// callback, which makes two GitLab calls bounded at 10 seconds each, so
	// this leaves it room while still cutting off a client that stops
	// reading.
	writeTimeout = 30 * time.Second
	// idleTimeout bounds how long a keep-alive connection may sit unused
	// before the server closes it.
	idleTimeout = 2 * time.Minute
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
		// A failure inside RunE is a runtime problem, not a misuse of the
		// CLI, so printing the flag list at it helps nobody and costs a
		// great deal: the usage output renders every flag's default, and
		// the credential flags default to what the environment supplied.
		// A single timed-out GitLab call would otherwise put the write
		// token, the webhook secret and the database DSN into the logs.
		SilenceUsage: true,
		// Execute prints the error itself, with the same treatment for
		// every subcommand; letting cobra print it too only duplicates it.
		SilenceErrors: true,
		// Inherited by every subcommand, none of which defines its own.
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return checkBoolEnv()
		},
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
	secretFlag(flags, &cfg.GitLabReadToken, "gitlab-read-token", "GITLAB_READ_TOKEN", "Read-only GitLab token (read_api scope)")
	secretFlag(flags, &cfg.GitLabWriteToken, "gitlab-write-token", "GITLAB_WRITE_TOKEN", "Write-capable GitLab token (api scope), scoped down by role")
	flags.StringVar(&cfg.AchievementsNamespace, "achievements-namespace", os.Getenv("ACHIEVEMENTS_NAMESPACE"), "Full path of the namespace that owns the achievement definitions")
	secretFlag(flags, &cfg.DatabaseDSN, "database-dsn", "DATABASE_DSN", fmt.Sprintf("Database connection string (postgres://, sqlite://, mysql://, or sqlserver://); defaults to %q", config.DefaultDatabaseDSN))
	secretFlag(flags, &cfg.WebhookSecret, "webhook-secret", "WEBHOOK_SECRET", "Secret token used to validate incoming GitLab webhook deliveries")
	flags.StringVar(&cfg.PublicURL, "public-url", os.Getenv("PUBLIC_URL"), "Externally reachable base URL of this app, used to register its GitLab webhooks")
	flags.StringVar(&cfg.ListenAddr, "listen-addr", envOrDefault("LISTEN_ADDR", config.DefaultListenAddr), "Address the HTTP server listens on")
	flags.StringVar(&cfg.MetricsAddr, "metrics-addr", envOrDefault("METRICS_ADDR", config.DefaultMetricsAddr), "Address Prometheus metrics are served on, apart from --listen-addr; \"off\" serves none")
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
	flags.BoolVar(&cfg.ExcludeBots, "exclude-bots", envBoolOrDefault(envExcludeBots, true), "Leave out accounts GitLab flags as bots: access token users, service accounts, GitLab's own bots")
	flags.StringSliceVar(&cfg.ExcludeUsers, "exclude-users", envListOrDefault("EXCLUDE_USERS"), "Usernames or GitLab user IDs to leave out of achievements, comma-separated")
	flags.StringVar(&cfg.ExcludeUsernamePattern, "exclude-username-pattern", os.Getenv("EXCLUDE_USERNAME_PATTERN"), "Regular expression (RE2); users whose username matches are left out of achievements")
	flags.BoolVar(&cfg.AllowOptOut, "allow-opt-out", envBoolOrDefault(envAllowOptOut, true), "Let users leave themselves out through the API (needs --api-auth gitlab)")
	secretFlag(flags, &cfg.OAuthClientSecret, "oauth-client-secret", "OAUTH_CLIENT_SECRET", "Client secret for --oauth-client-id, making it a confidential client; empty means PKCE alone")
	secretFlag(flags, &cfg.SessionSecret, "session-secret", "SESSION_SECRET", "Key for encrypting the GitLab tokens browser sessions hold, at least 32 characters; required when --api-auth is gitlab")
	flags.DurationVar(&cfg.SessionLifetime, "session-lifetime", envDurationOrDefault("SESSION_LIFETIME", config.DefaultSessionLifetime), "How long a browser stays logged in; sessions renew their GitLab token as it runs out")
	flags.DurationVar(&cfg.APIAuthCacheTTL, "api-auth-cache-ttl", envDurationOrDefault("API_AUTH_CACHE_TTL", config.DefaultAPIAuthCacheTTL), "How long a verified GitLab credential is trusted before asking GitLab again (also how long a revoked token keeps working); 0 disables")
	flags.StringVar(&cfg.APIAllowedGroup, "api-allowed-group", os.Getenv("API_ALLOWED_GROUP"), "Full path of a GitLab group; only its members (and instance admins) may use the API")
	flags.StringSliceVar(&cfg.OAuthReturnOrigins, "oauth-return-origins", envListOrDefault("OAUTH_RETURN_ORIGINS"), "Origins besides --public-url's that a login may return to, comma-separated, e.g. https://rankings.example.com")
	flags.StringVar(&cfg.FrontendDir, "frontend-dir", os.Getenv("FRONTEND_DIR"), "Directory of a built static frontend to serve at /, on the same origin as the API")
}

// secretFlag registers a flag whose value is a credential, taking its
// value from env when the flag is absent.
//
// The environment value is assigned to target after registration rather
// than handed to pflag as the flag's default, because pflag keeps the
// default in DefValue and renders it in the usage output as
// `(default "...")`. Registering the real token as a default is enough to
// put it in front of anyone running --help, and in front of anyone reading
// the logs of a command that printed its usage.
//
// pflag only writes to target when the flag actually appears on the
// command line, so seeding it here does not stop an explicit flag from
// winning, and an empty default is omitted from the usage output entirely.
func secretFlag(flags *pflag.FlagSet, target *string, name, env, usage string) {
	flags.StringVar(target, name, "", usage)

	*target = os.Getenv(env)
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

// The boolean environment variables, named once so that checkBoolEnv
// covers every one envBoolOrDefault reads.
const (
	envExcludeBots = "EXCLUDE_BOTS"
	envAllowOptOut = "ALLOW_OPT_OUT"
)

// envBoolOrDefault returns the environment variable key parsed as a
// boolean, or fallback when it is unset or unparsable. An unparsable value
// is reported by checkBoolEnv before anything runs.
func envBoolOrDefault(key string, fallback bool) bool {
	parsed, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}

	return parsed
}

// checkBoolEnv rejects a boolean environment variable that is set but not a
// boolean strconv.ParseBool accepts.
//
// Unlike a bad number or duration, a bad boolean has no invalid value for
// Validate to catch: envBoolOrDefault falls back to the default, so
// ALLOW_OPT_OUT=no would quietly leave opting out on.
func checkBoolEnv() error {
	var errs []error

	for _, key := range []string{envExcludeBots, envAllowOptOut} {
		value := os.Getenv(key)
		if value == "" {
			continue
		}

		_, err := strconv.ParseBool(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s=%q is not a boolean, use true or false", key, value))
		}
	}

	return errors.Join(errs...)
}

// envListOrDefault returns the environment variable key split on commas,
// with blank entries dropped, or nil when it is unset.
func envListOrDefault(key string) []string {
	var entries []string

	for entry := range strings.SplitSeq(os.Getenv(key), ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			entries = append(entries, entry)
		}
	}

	return entries
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

	metrics.SetBuildInfo(resolveVersion())

	// Started before bootstrap rather than alongside the main server, so a
	// first start that spends minutes registering hooks is already
	// observable, reporting itself not ready, instead of silent.
	stopMetrics, err := startMetricsServer(cfg)
	if err != nil {
		return err
	}

	defer stopMetrics()

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

// startMetricsServer serves Prometheus metrics on cfg.MetricsAddr, returning
// a function that stops it.
//
// The address is bound here, synchronously, so a port already in use fails
// the start with an error naming it instead of leaving the app running
// without the metrics it was configured to serve. Once bound, a later
// failure only loses the metrics and is logged rather than taking the
// awarder down with it.
func startMetricsServer(cfg *config.Config) (func(), error) {
	if !cfg.MetricsEnabled() {
		zap.L().Info("metrics disabled")

		return func() {}, nil
	}

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", cfg.MetricsAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen for metrics on %q: %w", cfg.MetricsAddr, err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	go func() {
		zap.L().Info("serving metrics", zap.String("addr", listener.Addr().String()))

		serveErr := srv.Serve(listener)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			zap.L().Error("metrics server stopped", zap.Error(serveErr))
		}
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		shutdownErr := srv.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			zap.L().Warn("failed to shut down metrics server gracefully", zap.Error(shutdownErr))
		}
	}, nil
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
		zap.Int("achievements_adopted", report.Achievements.Adopted),
		zap.Int("achievements_updated", report.Achievements.Updated),
		zap.Int("achievements_recreated", report.Achievements.Recreated),
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
func buildAPI(
	ctx context.Context,
	cfg *config.Config,
	conn *gorm.DB,
	readClient *gitlabclient.ReadClient,
	writeClient *gitlabclient.WriteClient,
	filter *exclusion.Filter,
) (*api.API, error) {
	opts := api.Options{
		Exclusions:       filter,
		SessionSecret:    cfg.SessionSecret,
		SessionLifetime:  cfg.SessionLifetime,
		IdentityCacheTTL: cfg.APIAuthCacheTTL,
		Instance:         api.Instance{GitLabURL: cfg.GitLabURL, Version: resolveVersion()},
	}

	if cfg.AuthMode() != config.APIAuthGitLab {
		zap.L().Info("read api is unauthenticated", zap.String("api_auth", cfg.APIAuth))

		return newAPI(conn, opts)
	}

	verifier, err := gitlabclient.NewTokenVerifier(cfg.GitLabURL)
	if err != nil {
		return nil, fmt.Errorf("failed to build gitlab token verifier: %w", err)
	}

	opts.Verifier = verifier

	if cfg.APIAllowedGroup != "" {
		opts.IsMember, err = membershipChecker(ctx, readClient, cfg.APIAllowedGroup)
		if err != nil {
			return nil, err
		}
	}

	clientID := cfg.OAuthClientID
	if clientID == "" {
		clientID, err = bootstrap.EnsureOAuthApplication(ctx, writeClient, conn, cfg.PublicURL+api.CallbackPath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve the oauth application for the read api: %w", err)
		}
	}

	opts.OAuth = oauthOptions(cfg, clientID)

	zap.L().Info("read api requires a gitlab credential",
		zap.String("oauth_client_id", clientID),
		zap.Bool("confidential_client", cfg.OAuthClientSecret != ""),
		zap.String("allowed_group", cfg.APIAllowedGroup),
		zap.Duration("identity_cache_ttl", cfg.APIAuthCacheTTL),
		zap.Duration("session_lifetime", cfg.SessionLifetime),
	)

	return newAPI(conn, opts)
}

// oauthOptions configures the login flow through the OAuth application
// clientID names.
func oauthOptions(cfg *config.Config, clientID string) *api.OAuthOptions {
	return &api.OAuthOptions{
		GitLabURL:     cfg.GitLabURL,
		PublicURL:     cfg.PublicURL,
		ClientID:      clientID,
		ClientSecret:  cfg.OAuthClientSecret,
		ReturnOrigins: cfg.OAuthReturnOrigins,
		Landing:       loginLanding(cfg),
	}
}

// loginLanding is where a login with no return_to lands, matching the
// frontend buildFrontend serves: the built-in one's page for the user who
// logged in, the root of an operator's own (whose routes this app cannot
// know), or, with no frontend at all, nil for the user's record in the API.
func loginLanding(cfg *config.Config) func(username string) string {
	switch {
	case cfg.FrontendDir != "":
		return func(string) string { return "/" }
	case frontend.HasIndex(web.Dist()):
		return func(username string) string { return "/users/" + url.PathEscape(username) }
	default:
		return nil
	}
}

// membershipChecker restricts the API to group's members.
//
// A membership lookup on a group that does not exist (or that the read
// token cannot see) answers 404 just as one for a non-member does, so a
// typo in the group would quietly refuse everybody. Checked once here, it
// fails the start instead. Any other error is GitLab being unreachable,
// which is not worth refusing to start over.
func membershipChecker(ctx context.Context, readClient *gitlabclient.ReadClient, group string) (api.MembershipChecker, error) {
	_, err := readClient.GetGroup(group, nil, gitlab.WithContext(ctx))
	if gitlabclient.IsNotFound(err) {
		return nil, fmt.Errorf("api-allowed-group %q does not exist or is not visible to the read token", group)
	}

	if err != nil {
		zap.L().Warn("could not check that api-allowed-group exists", zap.String("group", group), zap.Error(err))
	}

	return func(ctx context.Context, gitlabUserID int64) (bool, error) {
		return readClient.IsGroupMember(ctx, group, gitlabUserID)
	}, nil
}

// newAPI builds the API, wrapping its construction error.
func newAPI(conn *gorm.DB, opts api.Options) (*api.API, error) {
	readAPI, err := api.New(conn, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to build the read api: %w", err)
	}

	return readAPI, nil
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
	filter *exclusion.Filter,
) (*http.Server, error) {
	readAPI, err := buildAPI(ctx, cfg, conn, readClient, writeClient, filter)
	if err != nil {
		return nil, err
	}

	site, err := buildFrontend(cfg)
	if err != nil {
		return nil, err
	}

	startSessionPruning(ctx, cfg, readAPI)

	receiver := webhook.NewReceiver(cfg.WebhookSecret, queue)

	return newHTTPServer(cfg, sqlDB, readClient, receiver, readAPI, site), nil
}

// buildFrontend builds the handler for the frontend: the directory
// FRONTEND_DIR names when set, or else the one compiled into the binary.
// It is nil for a binary compiled without building the frontend, which
// then serves the API alone. A FRONTEND_DIR that is not a built frontend
// fails the start rather than serving 404s at "/".
func buildFrontend(cfg *config.Config) (http.Handler, error) {
	if cfg.FrontendDir != "" {
		site, err := frontend.Handler(cfg.FrontendDir)
		if err != nil {
			return nil, fmt.Errorf("failed to serve the frontend: %w", err)
		}

		zap.L().Info("serving frontend", zap.String("dir", cfg.FrontendDir))

		return site, nil
	}

	if !frontend.HasIndex(web.Dist()) {
		zap.L().Info("this binary was built without its frontend; serving the api alone")

		return nil, nil //nolint:nilnil // no frontend is a valid configuration, not an error
	}

	site, err := frontend.HandlerFS(web.Dist(), frontend.Options{
		ContentSecurityPolicy: builtinPolicy(cfg.GitLabURL),
		ImmutableDir:          "assets",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to serve the built-in frontend: %w", err)
	}

	zap.L().Info("serving the built-in frontend")

	return site, nil
}

// builtinPolicy is the Content Security Policy the built-in frontend runs
// under. Everything it loads is its own, except the avatars in its
// rankings, which come from wherever GitLab says they live: the instance
// itself, or a Gravatar-style service it is configured with.
func builtinPolicy(gitlabURL string) string {
	images := "'self' data: https:"
	if gitlab, ok := origin.Of(gitlabURL); ok {
		images += " " + gitlab
	}

	return "default-src 'self'; img-src " + images + "; object-src 'none'; " +
		"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}

// newHTTPServer builds the *http.Server exposing /healthz, /readyz, the
// webhook ingestion endpoint, and the read API, wired to check the database
// connection and GitLab reachability on every /readyz request.
func newHTTPServer(cfg *config.Config, sqlDB *sql.DB, readClient *gitlabclient.ReadClient, receiver http.Handler, readAPI *api.API, site http.Handler) *http.Server {
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

	// The frontend claims everything no other route does, so it can never
	// shadow the API, the login flow, the webhook, or the probes. It is
	// mounted without a method: "GET /" would conflict with the method-less
	// API subtrees, being neither more nor less specific than them, and
	// the frontend refuses other methods itself.
	if site != nil {
		srv.Mount("/", site)
	}

	srv.SetReady(true)

	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           metrics.Middleware(srv.Handler()),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
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
	queue, filter, err := startQueue(ctx, cfg, conn, readClient)
	if err != nil {
		return err
	}

	httpSrv, err := buildServer(ctx, cfg, conn, sqlDB, readClient, writeClient, queue, filter)
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

// startQueue starts the queue live deliveries are evaluated through, behind
// an engine that skips excluded users, and returns the exclusion filter
// alongside it for the API's opt-out endpoints to share. The background
// sweep classifying users not yet checked for being bots starts with it.
func startQueue(ctx context.Context, cfg *config.Config, conn *gorm.DB, readClient *gitlabclient.ReadClient) (*webhook.Queue, *exclusion.Filter, error) {
	filter, err := applyExclusions(ctx, cfg, conn, readClient)
	if err != nil {
		return nil, nil, err
	}

	startBotSweep(ctx, cfg, conn)

	queue := webhook.NewQueue(engine.New(conn, engine.WithFilter(filter)), webhook.Options{})
	queue.Start(ctx)

	return queue, filter, nil
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

	go scheduler.Every(ctx, api.PruneInterval, metrics.Job("session_prune", func(ctx context.Context) error {
		pruned, err := readAPI.PruneSessions(ctx)
		if err != nil {
			return fmt.Errorf("session pruning failed: %w", err)
		}

		if pruned > 0 {
			zap.L().Info("pruned expired api sessions", zap.Int64("sessions", pruned))
		}

		return nil
	}), func(err error) {
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
	go scheduler.Every(ctx, webhookReconcileInterval, metrics.Job("webhook_reconcile", func(ctx context.Context) error {
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
	}), func(err error) {
		zap.L().Error("webhook reconciliation failed", zap.Error(err))
	})

	go scheduler.Every(ctx, achievementReconcileInterval, metrics.Job("achievement_reconcile", func(ctx context.Context) error {
		hourly, err := bootstrap.RunHourlyReconciliation(ctx, writeClient, conn, namespaceID, cfg.AchievementsNamespace)
		if err != nil {
			return fmt.Errorf("achievement reconciliation failed: %w", err)
		}

		zap.L().Info("achievement reconciliation complete",
			zap.Int("achievements_recreated", hourly.Achievements.Recreated),
			zap.Int("achievements_adopted", hourly.Achievements.Adopted),
			zap.Int("achievements_updated", hourly.Achievements.Updated),
			zap.Int("achievements_unchanged", hourly.Achievements.Unchanged),
			zap.Int("exp_totals_corrected", hourly.ExpTotalsCorrected),
			zap.Int("awards_confirmed", hourly.Awards.Confirmed),
			zap.Int("awards_failed", hourly.Awards.Failed),
			zap.Int("awards_superseded", hourly.Awards.Superseded),
			zap.Int("awards_adopted", hourly.Awards.Adopted),
		)

		return nil
	}), func(err error) {
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
