package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/backfill"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/engine"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
	"github.com/boxboxjason/gitlab-achievements/internal/logging"
	"github.com/boxboxjason/gitlab-achievements/internal/reconcile"
	"github.com/boxboxjason/gitlab-achievements/internal/scheduler"
)

// reconcileStartupDelay is how long the in-process sync waits before its
// first pass; see startupDelay.
const reconcileStartupDelay = 5 * time.Minute

// errNotBootstrapped reports that the database holds no achievement
// definitions, so there is nothing for a reconciliation pass to award
// against. It is its own error because the fix is an operator action
// (start the server, or run `backfill`, once) rather than a retry.
var errNotBootstrapped = errors.New("no achievement definitions in the database: " +
	"start the server or run `gitlab-achievements backfill` once before reconciling")

// buildReconcileCmd builds the subcommand that runs one reconciliation pass
// and exits, for operators who would rather the sweep be an external
// scheduled job (a Kubernetes CronJob, a systemd timer) than a goroutine in
// a serving pod. Pair it with `--reconcile=off` on the serving deployment
// so the two don't both sweep.
//
// It is also what a horizontally scaled deployment needs: the in-process
// timer runs in every replica, so several of them would sweep the instance
// at once, each re-reading a window the others had already covered. One
// CronJob against the shared database replaces all of them.
func buildReconcileCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile",
		Short: "Re-read recent instance activity once, healing anything the webhooks dropped",
		Long: "Re-reads the last --reconcile-lookback of instance activity through the Events API and replays it " +
			"through the achievement engine, so activity whose webhook delivery was lost (GitLab downtime, a " +
			"network blip, a deploy) still earns achievements.\n\n" +
			"Activity that was already counted is discarded rather than counted again: both paths derive the same " +
			"dedup key for the same activity, so a pass over a window the webhooks covered correctly is a no-op.\n\n" +
			"Unlike the server and the `backfill` subcommand this does not bootstrap: it reads GitLab and writes " +
			"awards, but registers no webhooks and creates no achievements, so running it on a schedule costs one " +
			"sweep of recently active projects rather than a sweep of the whole instance.\n\n" +
			"The window widens on its own to cover everything since the last successful pass, so a run that was " +
			"missed or that failed is made up by the next one rather than leaving a hole.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runReconcile(cfg)
		},
	}
}

// runReconcile is the `reconcile` subcommand's entry point.
//
// Unlike the serving process it ignores --reconcile=off: invoking the
// subcommand is itself the instruction to run, and off is precisely the
// setting a deployment scheduling this externally is expected to use.
func runReconcile(cfg *config.Config) error {
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

	err = requireBootstrapped(conn)
	if err != nil {
		return err
	}

	writeClient, err := gitlabclient.NewWriteClient(cfg.GitLabURL, cfg.GitLabWriteToken)
	if err != nil {
		return fmt.Errorf("failed to build gitlab write client: %w", err)
	}

	return executeReconcile(ctx, cfg, conn, writeClient)
}

// startReconcileLoop launches the periodic reconciliation sync in the
// background of the serving process, unless the operator turned it off.
//
// It runs a pass shortly after startup and then on the configured interval,
// rather than waiting a full interval for the first one. The wait is not an
// option: the ticker's phase is the process's start time, so a deployment
// restarted more often than the interval — a daily rollout against the
// default 24h — would never reconcile at all, and would never say so. A
// sync nothing can tell has stopped running is worse than no sync.
//
// Starting from a restart is also cheap, which is what makes this safe to
// do every time: the watermark means the pass covers the gap since the last
// successful one rather than the whole look-back, and the project listing
// is narrowed to what GitLab reports as recently active.
func startReconcileLoop(ctx context.Context, cfg *config.Config, conn *gorm.DB, writeClient *gitlabclient.WriteClient) {
	if config.ReconcileMode(cfg.ReconcileMode) == config.ReconcileModeOff {
		zap.L().Info("periodic reconciliation disabled, expecting an external `gitlab-achievements reconcile` schedule instead")

		return
	}

	delay := startupDelay(cfg.ReconcileInterval)

	zap.L().Info("periodic reconciliation scheduled",
		zap.Duration("interval", cfg.ReconcileInterval),
		zap.Duration("lookback", cfg.ReconcileLookback),
		zap.Duration("first_pass_in", delay),
	)

	pass := func(ctx context.Context) error {
		return executeReconcile(ctx, cfg, conn, writeClient)
	}

	onError := func(err error) {
		zap.L().Error("activity reconciliation failed, the next pass covers the same window", zap.Error(err))
	}

	go func() {
		if !sleepOrDone(ctx, delay) {
			return
		}

		err := pass(ctx)
		if err != nil && ctx.Err() == nil {
			onError(err)
		}

		// scheduler.Every expects its caller to have done the initial run,
		// which is exactly what the pass above is.
		scheduler.Every(ctx, cfg.ReconcileInterval, pass, onError)
	}()
}

// startupDelay is how long the first pass waits after the process starts.
//
// Not zero, because a restart is the moment the app is busiest: bootstrap
// has just swept the instance's hooks, and the historical backfill may
// still be walking. A few minutes is enough for those to be past their
// opening burst without being long enough for a rollout to cut the pass off
// before it happens.
//
// It never exceeds the interval, so a deployment that reconciles more often
// than that still gets its first pass on schedule rather than late.
func startupDelay(interval time.Duration) time.Duration {
	return min(reconcileStartupDelay, interval)
}

// sleepOrDone waits for d, reporting false if ctx was cancelled first.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// executeReconcile runs one pass and delivers whatever awards it produced.
//
// The pass gets a read client of its own, rate-limited like the backfill's
// and for the same reason: it is an instance-wide read sweep against a
// GitLab this app doesn't own, and a readiness check shouldn't have to
// queue behind its request budget.
func executeReconcile(ctx context.Context, cfg *config.Config, conn *gorm.DB, writeClient *gitlabclient.WriteClient) error {
	ready, err := historyWalked(conn)
	if err != nil {
		return err
	}

	// Reconciliation is the steady-state correction on top of a complete
	// picture, so it waits for one. Running it against a half-walked
	// instance would work — the engine would count what it found — but it
	// would put a second instance-wide read sweep alongside the cold start
	// to cover a window the cold start is about to reach anyway.
	if !ready {
		zap.L().Info("skipping activity reconciliation until the historical backfill has completed")

		return nil
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

	report, err := reconcile.Run(ctx, readClient, conn, achievements, reconcile.Options{
		Lookback: cfg.ReconcileLookback,
	})
	if err != nil {
		return fmt.Errorf("activity reconciliation failed: %w", err)
	}

	logReconcileReport(report, achievements.Stats())

	return deliverPendingAwards(ctx, writeClient, conn, "reconciliation")
}

// logReconcileReport records what a pass covered and what it recovered.
//
// activity_counted is the number that matters: it is what the webhooks
// dropped, and in a healthy deployment it is zero pass after pass while
// activity_skipped tracks how much was correctly counted the first time.
// A persistently non-zero count means deliveries are being lost, which is
// worth looking into rather than leaving to this to paper over.
func logReconcileReport(report *reconcile.Report, stats engine.Stats) {
	fields := []zap.Field{
		zap.Time("since", report.Since),
		zap.Time("until", report.Until),
		zap.Int("projects", report.Projects),
		zap.Int("projects_skipped", report.ProjectsSkipped),
		zap.Int("events", report.Events),
		zap.Int("pipelines", report.Pipelines),
		zap.Int64("activity_counted", stats.Processed),
		zap.Int64("activity_skipped", stats.Skipped),
		zap.Int64("achievements_earned", stats.Awarded),
	}

	if report.Gap > 0 {
		zap.L().Warn("activity reconciliation complete, having made up for missed passes",
			append(fields, zap.Duration("gap", report.Gap))...)

		return
	}

	zap.L().Info("activity reconciliation complete", fields...)
}

// historyWalked reports whether the instance's history has been walked end
// to end, which is what the reconciliation sync waits for.
func historyWalked(conn *gorm.DB) (bool, error) {
	_, done, err := backfill.CompletedAt(conn)
	if err != nil {
		return false, fmt.Errorf("failed to read the backfill completion watermark: %w", err)
	}

	return done, nil
}

// requireBootstrapped fails fast when the database has never been
// bootstrapped, so a scheduled `reconcile` against a fresh database says so
// instead of reading the whole instance and awarding nothing.
func requireBootstrapped(conn *gorm.DB) error {
	var definitions int64

	err := conn.Model(&db.AchievementDefinition{}).Count(&definitions).Error
	if err != nil {
		return fmt.Errorf("failed to count achievement definitions: %w", err)
	}

	if definitions == 0 {
		return errNotBootstrapped
	}

	return nil
}
