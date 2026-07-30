// Package bootstrap self-configures everything this app needs on the
// GitLab side on startup: it verifies both tokens actually have the access
// they claim, then idempotently creates/updates the achievement catalog
// and registers the system webhook that feeds event ingestion.
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
)

// Client is everything bootstrap needs from the GitLab read and write
// clients, satisfied by *gitlabclient.ReadClient and *gitlabclient.WriteClient.
type Client struct {
	Read  readVerifier
	Write interface {
		writeVerifier
		achievementWriter
		hookManager
	}
}

// Report summarizes what a bootstrap run did.
type Report struct {
	NamespaceID  int64
	Achievements AchievementsReport
	Webhook      WebhookReport
}

// Run verifies permissions and idempotently reconciles the achievement
// catalog and system hook against the configured GitLab instance.
// webhookURL is the fully-qualified URL GitLab should deliver system hook
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

	webhook, err := syncSystemHook(ctx, clients.Write, conn, webhookURL, cfg.WebhookSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to sync system hook: %w", err)
	}

	return &Report{
		NamespaceID:  namespaceID,
		Achievements: achievements,
		Webhook:      webhook,
	}, nil
}

// HourlyReport summarizes what RunHourlyReconciliation did.
type HourlyReport struct {
	Achievements AchievementsReport
	Awards       AwardsReport
}

// RunHourlyReconciliation re-checks achievement existence and retries any
// award GitLab hasn't yet confirmed. It is meant to be called on a
// recurring ~1h cadence (see ReconcileWebhook for the equivalent ~5m
// webhook check), using the namespace ID resolved once by an earlier Run.
// namespaceFullPath must be the same namespace the ID was resolved from
// (cfg.AchievementsNamespace), since listing achievements is a GraphQL
// lookup keyed by full path rather than numeric ID.
func RunHourlyReconciliation(ctx context.Context, write achievementWriter, conn *gorm.DB, namespaceID int64, namespaceFullPath string) (*HourlyReport, error) {
	achievements, err := ReconcileAchievements(ctx, write, conn, namespaceID, namespaceFullPath, catalog.V1())
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile achievement definitions: %w", err)
	}

	awards, err := ReconcileAwards(ctx, write, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile award status: %w", err)
	}

	return &HourlyReport{
		Achievements: achievements,
		Awards:       awards,
	}, nil
}
