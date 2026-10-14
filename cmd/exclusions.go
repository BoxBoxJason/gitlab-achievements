package cmd

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
	"github.com/boxboxjason/gitlab-achievements/internal/metrics"
)

// buildExclusionFilter builds the filter deciding who takes part, asking
// GitLab through lookup whether an account is a bot.
//
// Self-service opt-out is only offered where the API knows who is asking.
// Without authentication there is no caller identity to opt out, so the
// endpoints would have nothing to act on.
func buildExclusionFilter(cfg *config.Config, conn *gorm.DB, lookup exclusion.BotLookup) (*exclusion.Filter, error) {
	policy, err := exclusion.NewPolicy(cfg.ExcludeBots, cfg.ExcludeUsers, cfg.ExcludeUsernamePattern)
	if err != nil {
		return nil, fmt.Errorf("invalid exclusion configuration: %w", err)
	}

	allowOptOut := cfg.AllowOptOut && cfg.AuthMode() == config.APIAuthGitLab

	return exclusion.NewFilter(conn, policy, lookup, allowOptOut), nil
}

// applyExclusions builds a filter and re-decides every tracked user against
// it, so a user the operator just excluded is already off the leaderboard
// by the time the server answers, and has nothing pushed to GitLab by a
// one-off backfill or reconcile run's award delivery. lookup may be nil:
// re-deciding asks GitLab nothing.
func applyExclusions(ctx context.Context, cfg *config.Config, conn *gorm.DB, lookup exclusion.BotLookup) (*exclusion.Filter, error) {
	filter, err := buildExclusionFilter(cfg, conn, lookup)
	if err != nil {
		return nil, err
	}

	changed, err := filter.Apply(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to apply user exclusions: %w", err)
	}

	zap.L().Info("user exclusions applied",
		zap.Bool("exclude_bots", cfg.ExcludeBots),
		zap.Int("excluded_users_configured", len(cfg.ExcludeUsers)),
		zap.Bool("exclude_username_pattern", cfg.ExcludeUsernamePattern != ""),
		zap.Bool("opt_out_allowed", filter.AllowsOptOut()),
		zap.Int("users_reclassified", changed),
	)

	return filter, nil
}

// startBotSweep asks GitLab, once and in the background, about every
// tracked user not yet classified as a bot or not: everyone recorded before
// bot exclusion existed, and anyone GitLab could not be asked about when
// they were first seen.
//
// It reads through a client paced like the backfill's, since on an
// instance with history it is a lookup per user.
func startBotSweep(ctx context.Context, cfg *config.Config, conn *gorm.DB) {
	if !cfg.ExcludeBots {
		return
	}

	go func() {
		err := metrics.Job("bot_sweep", func(ctx context.Context) error {
			readClient, err := gitlabclient.NewReadClient(
				cfg.GitLabURL,
				cfg.GitLabReadToken,
				gitlabclient.WithRateLimit(cfg.BackfillRate, readRequestBurst),
			)
			if err != nil {
				return fmt.Errorf("failed to build rate-limited gitlab read client: %w", err)
			}

			filter, err := buildExclusionFilter(cfg, conn, readClient)
			if err != nil {
				return err
			}

			excluded, err := filter.SweepBots(ctx)
			if err != nil {
				return fmt.Errorf("bot sweep failed: %w", err)
			}

			zap.L().Info("bot sweep complete", zap.Int("bots_excluded", excluded))

			return nil
		})(ctx)
		if err != nil && ctx.Err() == nil {
			zap.L().Error("bot sweep failed, it runs again on the next start", zap.Error(err))
		}
	}()
}
