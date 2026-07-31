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

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the GitLab webhooks this app registered",
		Long: "Deletes every event ingestion webhook this app registered on the GitLab instance, using the records it " +
			"kept of what it registered and where.\n\n" +
			"Stop the serving deployment first: a running server re-registers the hooks on its next hourly " +
			"reconciliation sweep, so removing them underneath it accomplishes nothing.\n\n" +
			"Hooks whose group or project this app's write token may not manage are left in place and reported, " +
			"with their records kept, so re-running with a better-privileged token picks up exactly those.\n\n" +
			"This leaves the achievement definitions and the awards users earned alone: those are what people got out " +
			"of running this, and deleting them is a separate decision made in GitLab's own UI.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUninstall(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Sweep, "sweep", false,
		"Also enumerate the instance and remove hooks pointing at this app that it has no record of, for a lost or restored database")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false,
		"Report what would be removed without removing anything")

	return cmd
}

// runUninstall is the `uninstall` subcommand's entry point.
//
// It deliberately does not run bootstrap: that registers the very hooks
// this is here to remove. The clients are built directly, and permission
// verification is skipped too, since a token that can no longer do
// everything bootstrap demands should still get to clean up what it can.
func runUninstall(cfg *config.Config, opts bootstrap.CleanupOptions) error {
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

	report, err := bootstrap.RemoveWebhooks(ctx, readClient, writeClient, conn, cfg, cfg.PublicURL+httpserver.WebhookPath, opts, logger)
	if err != nil {
		return fmt.Errorf("failed to remove the event ingestion webhooks: %w", err)
	}

	logUninstallReport(report, opts, logger)

	return nil
}

// logUninstallReport records what the pass removed, naming the leftovers
// explicitly: a run that skipped hooks has not finished the job, and the
// operator is the only one who can hand it a token that will.
func logUninstallReport(report bootstrap.CleanupReport, opts bootstrap.CleanupOptions, logger *zap.Logger) {
	logger.Info("webhook cleanup complete",
		zap.Bool("dry_run", opts.DryRun),
		zap.Int("hooks_deleted", report.Deleted),
		zap.Int("hooks_already_gone", report.AlreadyGone),
		zap.Int("hooks_skipped", report.Skipped),
		zap.Int("targets_swept", report.Swept),
	)

	if report.Skipped > 0 {
		logger.Warn("some hooks were left registered on gitlab, re-run with a token that can manage them",
			zap.Int("hooks_skipped", report.Skipped),
		)
	}
}
