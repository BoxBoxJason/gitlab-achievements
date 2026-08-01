package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/backfill"
	"github.com/boxboxjason/gitlab-achievements/internal/bootstrap"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/engine"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
	"github.com/boxboxjason/gitlab-achievements/internal/httpserver"
	"github.com/boxboxjason/gitlab-achievements/internal/logging"
)

// readRequestBurst is how many requests the rate limiter in front of an
// instance-wide read sweep — the historical backfill, and the periodic
// reconciliation pass — lets through back to back. Kept at 1 so
// --backfill-rate describes the instantaneous rate and not just the
// long-run average: a burst would let the sweep arrive in spikes, which is
// exactly what a shared instance notices.
const readRequestBurst = 1

// buildBackfillCmd builds the subcommand that walks the instance's history
// once and exits, for operators who would rather the cold start be its own
// job (a Kubernetes Job, a one-off container) than something a serving pod
// does in the background. Pair it with `--backfill=off` on the serving
// deployment so the two don't both walk.
func buildBackfillCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "backfill",
		Short: "Walk the instance's history once, awarding achievements for past activity",
		Long: "Walks every project the read token can see and replays its history through the achievement engine, " +
			"so activity that happened before this app existed still earns achievements.\n\n" +
			"Bootstrap runs first, exactly as it does when serving: the walk needs the achievement definitions it " +
			"creates, and running it here keeps a one-off backfill job from depending on a serving instance having " +
			"started first.\n\n" +
			"The walk resumes from wherever a previous run stopped, and does nothing at all if history has already " +
			"been walked end to end. Pass --backfill=force to walk it again anyway.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runBackfill(cfg)
		},
	}
}

// runBackfill is the `backfill` subcommand's entry point: the same startup
// sequence the server performs, followed by the walk instead of serving.
//
// Unlike the serving process, this ignores --backfill=off: invoking the
// subcommand is itself the instruction to run. The flag still selects
// whether the completion watermark is honored (auto) or bypassed (force).
func runBackfill(cfg *config.Config) error {
	err := cfg.Validate()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	logger, err := logging.Setup(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("failed to set up logger: %w", err)
	}

	defer logger.Sync() //nolint:errcheck // ignore sync errors on exit

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, sqlDB, err := openDatabase(cfg)
	if err != nil {
		return err
	}

	defer sqlDB.Close() //nolint:errcheck // ignore close errors on exit

	_, writeClient, _, err := bootstrapApp(ctx, cfg, conn, cfg.PublicURL+httpserver.WebhookPath)
	if err != nil {
		return err
	}

	// No ceiling: a one-off walk is not running alongside this process's own
	// event ingestion, so there is no live path for it to overlap with. If a
	// serving instance is ingesting at the same time, the activity they both
	// see is the little that happens during the walk itself.
	return executeBackfill(ctx, cfg, conn, writeClient, time.Time{}, forceBackfill(cfg))
}

// startBackfill runs the historical walk in the background of the serving
// process, unless the operator turned it off.
//
// It deliberately does not block startup: a first walk of a large instance
// can take hours, and holding the HTTP server back that long would mean
// failing readiness probes and rejecting the webhook deliveries that are
// the app's actual job. A failure is logged rather than fatal, because the
// walk persists its cursor as it goes: the next start resumes it.
func startBackfill(ctx context.Context, cfg *config.Config, conn *gorm.DB, writeClient *gitlabclient.WriteClient, liveFrom time.Time) {
	if config.BackfillMode(cfg.BackfillMode) == config.BackfillModeOff {
		zap.L().Info("historical backfill disabled, expecting an explicit `gitlab-achievements backfill` run instead")

		return
	}

	go func() {
		err := executeBackfill(ctx, cfg, conn, writeClient, liveFrom, forceBackfill(cfg))
		if err != nil && ctx.Err() == nil {
			zap.L().Error("historical backfill failed, its progress is saved and it resumes on the next start", zap.Error(err))
		}
	}()
}

// executeBackfill walks the instance's history through the achievement
// engine, then delivers whatever awards it produced.
//
// The walk gets a read client of its own rather than reusing the one
// bootstrap and the readiness probe share, so the rate cap applies to the
// heavy workload only: a readiness check shouldn't have to queue behind a
// backfill's request budget.
func executeBackfill(
	ctx context.Context,
	cfg *config.Config,
	conn *gorm.DB,
	writeClient *gitlabclient.WriteClient,
	until time.Time,
	force bool,
) error {
	since, err := cfg.ParseBackfillSince()
	if err != nil {
		return fmt.Errorf("invalid backfill window: %w", err)
	}

	readClient, err := gitlabclient.NewReadClient(
		cfg.GitLabURL,
		cfg.GitLabReadToken,
		gitlabclient.WithRateLimit(cfg.BackfillRate, readRequestBurst),
	)
	if err != nil {
		return fmt.Errorf("failed to build rate-limited gitlab read client: %w", err)
	}

	achievements := engine.New(conn)

	zap.L().Info("historical backfill starting",
		zap.Float64("requests_per_second", cfg.BackfillRate),
		zap.Bool("force", force),
		zap.String("since", backfillSinceField(since)),
	)

	report, err := backfill.Run(ctx, readClient, conn, achievements, backfill.Options{
		Since: since,
		Until: until,
		Force: force,
	})
	if err != nil {
		return fmt.Errorf("historical backfill failed: %w", err)
	}

	if report.AlreadyComplete {
		zap.L().Info("historical backfill already complete, nothing to do",
			zap.Time("completed_at", report.CompletedAt),
		)

		return nil
	}

	logBackfillReport(report, achievements.Stats())

	return deliverPendingAwards(ctx, writeClient, conn, "backfill")
}

// logBackfillReport records what the walk covered and what it earned, in
// one place so the walk's own function stays about running it.
func logBackfillReport(report *backfill.Report, stats engine.Stats) {
	zap.L().Info("historical backfill complete",
		zap.Bool("resumed", report.Resumed),
		zap.Int("projects", report.Projects),
		zap.Int("projects_skipped", report.ProjectsSkipped),
		zap.Int("events", report.Events),
		zap.Int("pipelines", report.Pipelines),
		zap.Int64("activity_counted", stats.Processed),
		zap.Int64("activity_skipped", stats.Skipped),
		zap.Int64("achievements_earned", stats.Awarded),
	)
}

// deliverPendingAwards pushes the awards a read-side sweep recorded locally
// to GitLab straight away, rather than leaving them for the hourly award
// reconciliation to pick up. The engine records awards without calling
// GitLab (see its package doc), so without this a sweep that finished
// seconds after a reconciliation tick would sit invisible for an hour, and
// a one-off `backfill` or `reconcile` run would exit before ever delivering
// them.
//
// source names the sweep in the log line, since the two share this and a
// reader should be able to tell which one earned what.
func deliverPendingAwards(ctx context.Context, writeClient *gitlabclient.WriteClient, conn *gorm.DB, source string) error {
	awards, err := bootstrap.ReconcileAwards(ctx, writeClient, conn)
	if err != nil {
		return fmt.Errorf("failed to deliver awards earned by %s: %w", source, err)
	}

	zap.L().Info("awards delivered",
		zap.String("source", source),
		zap.Int("awards_confirmed", awards.Confirmed),
		zap.Int("awards_failed", awards.Failed),
		zap.Int("awards_superseded", awards.Superseded),
		zap.Int("awards_adopted", awards.Adopted),
	)

	return nil
}

// forceBackfill reports whether the configured mode says to walk history
// again even though it was already walked.
func forceBackfill(cfg *config.Config) bool {
	return config.BackfillMode(cfg.BackfillMode) == config.BackfillModeForce
}

// backfillSinceField renders the resolved look-back floor for logging,
// naming the unbounded case rather than logging a zero timestamp.
func backfillSinceField(since time.Time) string {
	if since.IsZero() {
		return "full history"
	}

	return since.Format(time.RFC3339)
}
