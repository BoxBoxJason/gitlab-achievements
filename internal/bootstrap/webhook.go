package bootstrap

import (
	"errors"
	"fmt"
	"strconv"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

const (
	gitlabAchievementsWebhookName = "gitlab-achievements"

	// systemHookIDStateKey is the db.SyncState key the system hook's GitLab
	// ID is stored under, so repeat checks (bootstrap and the periodic
	// reconciliation job alike) can look it up directly with GetSystemHook
	// instead of scanning every hook on the instance via ListSystemHooks.
	systemHookIDStateKey = "system_hook_id"
)

// hookManager is the subset of gitlabclient.WriteClient system hook
// synchronization needs.
type hookManager interface {
	ListSystemHooks(options ...gitlab.RequestOptionFunc) ([]*gitlab.Hook, error)
	GetSystemHook(hook int64, options ...gitlab.RequestOptionFunc) (*gitlab.Hook, error)
	AddSystemHook(opt *gitlab.AddHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Hook, error)
	EditSystemHook(hook int64, opt *gitlab.EditHookOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Hook, error)
}

// WebhookReport summarizes what syncSystemHook did.
type WebhookReport struct {
	HookID  int64
	Created bool
}

// ReconcileWebhook re-applies the same idempotent check syncSystemHook
// performs at bootstrap. It is safe to call repeatedly, e.g. from a
// periodic job, to heal a system hook that was altered or deleted after
// bootstrap ran.
func ReconcileWebhook(write hookManager, conn *gorm.DB, webhookURL, secret string) (WebhookReport, error) {
	return syncSystemHook(write, conn, webhookURL, secret)
}

// syncSystemHook idempotently registers the system hook this app ingests
// events from.
//
// The hook's GitLab ID is persisted in db.SyncState (see
// systemHookIDStateKey), so the common case is a direct GetSystemHook
// lookup by ID rather than a ListSystemHooks scan of the whole instance:
//
//   - stored ID found and GetSystemHook succeeds: the hook still exists,
//     possibly altered. EditSystemHook unconditionally re-applies the
//     desired URL/token/events, healing any drift.
//   - stored ID found but GetSystemHook returns 404: the hook was deleted;
//     fall through to the recovery path below.
//   - no stored ID (first-ever bootstrap, or upgrading from a version that
//     didn't persist one): fall back to scanning ListSystemHooks for a hook
//     matching webhookURL (handles adopting a hand-configured hook), or
//     register a new one if none match. Either way, the resulting ID is
//     persisted so future checks skip the scan.
//
// A transient error from GetSystemHook (network, permissions) is returned
// as-is rather than treated as "deleted", to avoid creating a duplicate
// hook on every hiccup.
func syncSystemHook(write hookManager, conn *gorm.DB, webhookURL, secret string) (WebhookReport, error) {
	storedID, found, err := loadWebhookID(conn)
	if err != nil {
		return WebhookReport{}, err
	}

	if found {
		report, ok, err := reuseStoredHook(write, storedID, webhookURL, secret)
		if err != nil {
			return WebhookReport{}, err
		}

		if ok {
			return report, nil
		}
	}

	return recoverSystemHook(write, conn, webhookURL, secret)
}

// reuseStoredHook attempts to reconcile the hook identified by storedID. The
// second return value is false only when the hook was confirmed deleted
// (404), signaling the caller should fall back to recoverSystemHook.
func reuseStoredHook(write hookManager, storedID int64, webhookURL, secret string) (WebhookReport, bool, error) {
	_, err := write.GetSystemHook(storedID)

	switch {
	case err == nil:
		edited, editErr := write.EditSystemHook(storedID, editHookOptions(webhookURL, secret))
		if editErr != nil {
			return WebhookReport{}, false, fmt.Errorf("failed to update existing system hook %d: %w", storedID, editErr)
		}

		return WebhookReport{HookID: edited.ID, Created: false}, true, nil
	case errors.Is(err, gitlab.ErrNotFound):
		return WebhookReport{}, false, nil
	default:
		return WebhookReport{}, false, fmt.Errorf("failed to check stored system hook %d: %w", storedID, err)
	}
}

// recoverSystemHook is the fallback path used when no hook ID is stored yet
// or the stored one was confirmed deleted: it scans existing hooks by URL
// before registering a new one, then persists whichever ID it lands on.
func recoverSystemHook(write hookManager, conn *gorm.DB, webhookURL, secret string) (WebhookReport, error) {
	hooks, err := write.ListSystemHooks()
	if err != nil {
		return WebhookReport{}, fmt.Errorf("failed to list system hooks: %w", err)
	}

	for _, existingHook := range hooks {
		if existingHook.URL != webhookURL {
			continue
		}

		edited, editErr := write.EditSystemHook(existingHook.ID, editHookOptions(webhookURL, secret))
		if editErr != nil {
			return WebhookReport{}, fmt.Errorf("failed to update existing system hook %d: %w", existingHook.ID, editErr)
		}

		storeErr := storeWebhookID(conn, edited.ID)
		if storeErr != nil {
			return WebhookReport{}, storeErr
		}

		return WebhookReport{HookID: edited.ID, Created: false}, nil
	}

	hook, err := write.AddSystemHook(addHookOptions(webhookURL, secret))
	if err != nil {
		return WebhookReport{}, fmt.Errorf("failed to register system hook: %w", err)
	}

	storeErr := storeWebhookID(conn, hook.ID)
	if storeErr != nil {
		return WebhookReport{}, storeErr
	}

	return WebhookReport{HookID: hook.ID, Created: true}, nil
}

// loadWebhookID reads the previously persisted system hook ID, if any.
func loadWebhookID(conn *gorm.DB) (int64, bool, error) {
	var state db.SyncState

	err := conn.Where("key = ?", systemHookIDStateKey).First(&state).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("failed to load stored system hook id: %w", err)
	}

	id, err := strconv.ParseInt(state.Value, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("stored system hook id %q is not numeric: %w", state.Value, err)
	}

	return id, true, nil
}

// storeWebhookID persists id as the current system hook ID, creating the
// db.SyncState row on first use and overwriting it thereafter.
func storeWebhookID(conn *gorm.DB, id int64) error {
	value := strconv.FormatInt(id, 10)

	var state db.SyncState

	err := conn.Where("key = ?", systemHookIDStateKey).First(&state).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		err = conn.Create(&db.SyncState{Key: systemHookIDStateKey, Value: value}).Error
	case err != nil:
		return fmt.Errorf("failed to load stored system hook id: %w", err)
	default:
		state.Value = value
		err = conn.Save(&state).Error
	}

	if err != nil {
		return fmt.Errorf("failed to persist system hook id: %w", err)
	}

	return nil
}

// addHookOptions and editHookOptions both enable push, tag, merge request,
// and repository update events, since the achievement catalog tracks that
// activity; system hooks always deliver the rest of the event set
// (user/project/group lifecycle) regardless of these flags.
func addHookOptions(webhookURL, secret string) *gitlab.AddHookOptions {
	return &gitlab.AddHookOptions{
		Name:                   new(gitlabAchievementsWebhookName),
		URL:                    &webhookURL,
		Token:                  &secret,
		PushEvents:             new(true),
		TagPushEvents:          new(true),
		MergeRequestsEvents:    new(true),
		RepositoryUpdateEvents: new(true),
		EnableSSLVerification:  new(true),
	}
}

func editHookOptions(webhookURL, secret string) *gitlab.EditHookOptions {
	return &gitlab.EditHookOptions{
		Name:                   new(gitlabAchievementsWebhookName),
		URL:                    &webhookURL,
		Token:                  &secret,
		PushEvents:             new(true),
		TagPushEvents:          new(true),
		MergeRequestsEvents:    new(true),
		RepositoryUpdateEvents: new(true),
		EnableSSLVerification:  new(true),
	}
}
