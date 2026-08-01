// Package bootstrap self-configures everything this app needs on the
// GitLab side on startup: it verifies both tokens actually have the access
// they claim, then idempotently creates/updates the achievement catalog
// and registers the webhooks that feed event ingestion.
//
// Bootstrap is strictly required: any failure here (bad permissions, a
// rejected mutation) is returned to the caller, which is expected to fail
// the process rather than serve traffic in a half-working state.
package bootstrap

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/engine"
)

// Client is everything bootstrap needs from the GitLab read and write
// clients, satisfied by *gitlabclient.ReadClient and *gitlabclient.WriteClient.
type Client struct {
	Read interface {
		readVerifier
		hookTargetLister
	}
	Write interface {
		writeVerifier
		achievementWriter
		hookManager
	}
}

// Report summarizes what a bootstrap run did.
type Report struct {
	Webhook      WebhookReport
	Achievements AchievementsReport
	NamespaceID  int64
	// ExpTotalsCorrected counts users whose stored EXP total had to be
	// re-derived because the catalog changed what a tier they already hold
	// is worth. It is zero on any run that didn't retune the catalog.
	ExpTotalsCorrected int
}

// Run verifies permissions and idempotently reconciles the achievement
// catalog and the event ingestion webhooks against the configured GitLab
// instance. webhookURL is the fully-qualified URL GitLab should deliver
// events to (the app's public URL plus its ingestion path).
func Run(ctx context.Context, clients Client, conn *gorm.DB, cfg *config.Config, webhookURL string) (*Report, error) {
	namespaceID, err := verifyPermissions(ctx, clients.Read, clients.Write, cfg.AchievementsNamespace)
	if err != nil {
		return nil, fmt.Errorf("permission verification failed: %w", err)
	}

	achievements, err := syncAchievements(ctx, clients.Write, conn, namespaceID, catalog.V1())
	if err != nil {
		return nil, fmt.Errorf("failed to sync achievement definitions: %w", err)
	}

	corrected, err := repairExpTotals(ctx, conn, achievements)
	if err != nil {
		return nil, err
	}

	webhook, err := syncHooks(ctx, clients.Read, clients.Write, conn, cfg, webhookURL)
	if err != nil {
		return nil, fmt.Errorf("failed to sync event ingestion webhooks: %w", err)
	}

	return &Report{
		NamespaceID:        namespaceID,
		Achievements:       achievements,
		Webhook:            webhook,
		ExpTotalsCorrected: corrected,
	}, nil
}

// repairExpTotals re-derives every user's EXP total when a definition sync
// changed one, and only then.
//
// A tier's EXP reward is stored on its definition, so retuning the catalog
// silently changes what tiers users already hold are worth. The engine
// keeps a total in step as awards land, but nothing about a retune awards
// anything, so without this a user who earns nothing new keeps the total
// the old catalog gave them indefinitely.
//
// Newly created definitions can't affect anyone: nobody holds an award for
// an achievement that didn't exist a moment ago. Updated and recreated ones
// can, so those are what trigger the sweep.
//
// This also covers the upgrade that introduced EXP: every pre-existing
// definition row comes out of the migration worth nothing, so the first
// sync after it reports them all as updated and every user's total is built
// from scratch on that same startup.
func repairExpTotals(ctx context.Context, conn *gorm.DB, achievements AchievementsReport) (int, error) {
	if achievements.Updated == 0 && achievements.Recreated == 0 {
		return 0, nil
	}

	corrected, err := engine.RecomputeAll(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("failed to recompute EXP totals after a catalog change: %w", err)
	}

	return corrected, nil
}

// HourlyReport summarizes what RunHourlyReconciliation did.
type HourlyReport struct {
	Achievements AchievementsReport
	Awards       AwardsReport
	// ExpTotalsCorrected counts users whose stored EXP total had to be
	// re-derived because a reconciled definition changed what a tier they
	// already hold is worth.
	ExpTotalsCorrected int
}

// RunHourlyReconciliation re-checks achievement existence and retries any
// award GitLab hasn't yet confirmed. It is meant to be called on a
// recurring ~1h cadence (see ReconcileWebhooks for the equivalent webhook
// sweep), using the namespace ID resolved once by an earlier Run.
// namespaceFullPath must be the same namespace the ID was resolved from
// (cfg.AchievementsNamespace), since listing achievements is a GraphQL
// lookup keyed by full path rather than numeric ID.
func RunHourlyReconciliation(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, namespaceFullPath string) (*HourlyReport, error) {
	achievements, err := ReconcileAchievements(ctx, write, conn, namespaceID, namespaceFullPath, catalog.V1())
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile achievement definitions: %w", err)
	}

	corrected, err := repairExpTotals(ctx, conn, achievements)
	if err != nil {
		return nil, err
	}

	awards, err := ReconcileAwards(ctx, write, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile award status: %w", err)
	}

	return &HourlyReport{
		Achievements:       achievements,
		Awards:             awards,
		ExpTotalsCorrected: corrected,
	}, nil
}
