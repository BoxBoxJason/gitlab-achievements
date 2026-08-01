package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/boxboxjason/gitlab-achievements/internal/bootstrap"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
	"github.com/boxboxjason/gitlab-achievements/internal/httpserver"
	"github.com/boxboxjason/gitlab-achievements/internal/logging"
)

// buildUninstallCmd builds the subcommand that removes this app's footprint
// from the GitLab instance's webhooks, for operators retiring a deployment.
//
// It is a subcommand rather than something the server does on shutdown for
// the reason bootstrap.RemoveWebhooks documents: a hook torn down on
// SIGTERM is registered again on the next start, so every rollout would
// churn every hook on the instance and lose the events arriving in between.
func buildUninstallCmd(cfg *config.Config) *cobra.Command {
	opts := bootstrap.CleanupOptions{}

	var keepAchievements bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the GitLab webhooks and achievements this app created",
		Long: "Deletes every event ingestion webhook this app registered on the GitLab instance, and every achievement " +
			"it created in the achievements namespace, using the records it kept of what it created and where.\n\n" +
			"Stop the serving deployment first: a running server re-registers the hooks on its next hourly " +
			"reconciliation sweep, and recreates the achievements on its next start, so removing them underneath it " +
			"accomplishes nothing.\n\n" +
			"Deleting the achievements takes the awards with them: GitLab removes an achievement from the profile of " +
			"everyone holding it, and nothing brings those back. Pass --keep-achievements to remove only the hooks " +
			"and leave people the badges they earned.\n\n" +
			"Hooks and achievements this app's write token may not manage are left in place and reported, with their " +
			"records kept, so re-running with a better-privileged token picks up exactly those.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUninstall(cfg, opts, keepAchievements)
		},
	}

	cmd.Flags().BoolVar(&opts.Sweep, "sweep", false,
		"Also enumerate the instance and remove hooks pointing at this app, and achievements in the namespace matching its catalog, that it has no record of, for a lost or restored database")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false,
		"Report what would be removed without removing anything")
	cmd.Flags().BoolVar(&keepAchievements, "keep-achievements", false,
		"Remove only the webhooks, leaving the achievement definitions and the awards users earned in place")

	return cmd
}

// runUninstall is the `uninstall` subcommand's entry point.
//
// It deliberately does not run bootstrap: that registers the very hooks
// this is here to remove. The clients are built directly, and permission
// verification is skipped too, since a token that can no longer do
// everything bootstrap demands should still get to clean up what it can.
func runUninstall(cfg *config.Config, opts bootstrap.CleanupOptions, keepAchievements bool) error {
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

	readClient, err := gitlabclient.NewReadClient(cfg.GitLabURL, cfg.GitLabReadToken)
	if err != nil {
		return fmt.Errorf("failed to build gitlab read client: %w", err)
	}

	writeClient, err := gitlabclient.NewWriteClient(cfg.GitLabURL, cfg.GitLabWriteToken)
	if err != nil {
		return fmt.Errorf("failed to build gitlab write client: %w", err)
	}

	report, err := bootstrap.RemoveWebhooks(ctx, readClient, writeClient, conn, cfg, cfg.PublicURL+httpserver.WebhookPath, opts)
	if err != nil {
		return fmt.Errorf("failed to remove the event ingestion webhooks: %w", err)
	}

	logUninstallReport(report, opts)

	// The hooks go first: with ingestion stopped, nothing new is earned
	// while the achievements are being deleted underneath it.
	if keepAchievements {
		zap.L().Info("leaving the achievements and the awards users earned in place",
			zap.Bool("keep_achievements", true),
		)

		return nil
	}

	achievements, err := bootstrap.RemoveAchievements(ctx, writeClient, conn, cfg, opts)
	if err != nil {
		return fmt.Errorf("failed to remove the achievement definitions: %w", err)
	}

	logAchievementCleanupReport(achievements, opts)

	return nil
}

// logUninstallReport records what the pass removed, naming the leftovers
// explicitly: a run that skipped hooks has not finished the job, and the
// operator is the only one who can hand it a token that will.
func logUninstallReport(report bootstrap.CleanupReport, opts bootstrap.CleanupOptions) {
	zap.L().Info("webhook cleanup complete",
		zap.Bool("dry_run", opts.DryRun),
		zap.Int("hooks_deleted", report.Deleted),
		zap.Int("hooks_already_gone", report.AlreadyGone),
		zap.Int("hooks_skipped", report.Skipped),
		zap.Int("targets_swept", report.Swept),
	)

	if report.Skipped > 0 {
		zap.L().Warn("some hooks were left registered on gitlab, re-run with a token that can manage them",
			zap.Int("hooks_skipped", report.Skipped),
		)
	}
}

// logAchievementCleanupReport records what the achievement pass removed, on
// the same terms as the hook one: leftovers are named, because a run that
// skipped some has not finished the job.
func logAchievementCleanupReport(report bootstrap.AchievementCleanupReport, opts bootstrap.CleanupOptions) {
	zap.L().Info("achievement cleanup complete",
		zap.Bool("dry_run", opts.DryRun),
		zap.Int("achievements_deleted", report.Deleted),
		zap.Int("achievements_already_gone", report.AlreadyGone),
		zap.Int("achievements_skipped", report.Skipped),
		zap.Int("achievements_swept", report.Swept),
	)

	if report.Skipped > 0 {
		zap.L().Warn("some achievements were left in the namespace, re-run with a token that can manage them",
			zap.Int("achievements_skipped", report.Skipped),
		)
	}
}
