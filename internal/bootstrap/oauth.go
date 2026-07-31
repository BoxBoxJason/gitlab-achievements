package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

const (
	// oauthClientIDStateKey is the db.SyncState key remembering which OAuth
	// application on the instance is this app's, so a restart adopts the
	// existing one instead of registering another.
	oauthClientIDStateKey = "oauth_application_client_id"
	// oauthApplicationName is the name the self-registered application is
	// given on GitLab, so an admin browsing the instance's applications can
	// tell what it belongs to.
	oauthApplicationName = "GitLab Achievements"
)

// oauthApplicationManager is what EnsureOAuthApplication needs from the
// write client, satisfied by *gitlabclient.WriteClient.
type oauthApplicationManager interface {
	ListOAuthApplications(options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlabclient.OAuthApplication, error]
	CreateOAuthApplication(name, redirectURI, scopes string, confidential bool, options ...gitlab.RequestOptionFunc) (*gitlabclient.OAuthApplication, error)
}

// EnsureOAuthApplication resolves the OAuth application the read API logs
// visitors in through, registering one on the instance if there isn't one
// already, and returns its client ID.
//
// The application is registered as a **public** client: GitLab returns a
// client secret exactly once, at creation, and no API can read it back, so
// an app that self-registers a confidential client would have to persist
// that secret or lose the ability to ever adopt its own application again.
// A public client has no secret to lose, and PKCE is what secures the code
// exchange instead. Operators who would rather run a confidential client
// register one by hand and configure its ID and secret, in which case this
// is never called.
//
// It is idempotent across restarts in two layers. The remembered client ID
// is checked against the instance first; if that application has been
// deleted out of band, or if the state row was lost while the application
// survived, the instance's applications are searched for one matching this
// app's redirect URI before anything new is created. Only when both miss is
// an application registered.
func EnsureOAuthApplication(ctx context.Context, write oauthApplicationManager, conn *gorm.DB, redirectURI string, logger *zap.Logger) (string, error) {
	remembered, err := loadOAuthClientID(ctx, conn)
	if err != nil {
		return "", err
	}

	existing, err := findOAuthApplication(ctx, write, remembered, redirectURI)
	if err != nil {
		return "", err
	}

	if existing != nil {
		if existing.ClientID != remembered {
			err = saveOAuthClientID(ctx, conn, existing.ClientID)
			if err != nil {
				return "", err
			}
		}

		logger.Info("adopted existing oauth application",
			zap.String("client_id", existing.ClientID),
			zap.String("redirect_uri", existing.CallbackURL),
		)

		return existing.ClientID, nil
	}

	created, err := write.CreateOAuthApplication(oauthApplicationName, redirectURI, gitlabclient.OAuthScopeReadUser, false, gitlab.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("failed to register an oauth application for the read api: %w", err)
	}

	err = saveOAuthClientID(ctx, conn, created.ClientID)
	if err != nil {
		return "", err
	}

	logger.Info("registered oauth application",
		zap.String("client_id", created.ClientID),
		zap.String("redirect_uri", redirectURI),
	)

	return created.ClientID, nil
}

// findOAuthApplication looks for this app's application on the instance,
// preferring the remembered client ID and falling back to whichever
// application is registered against this app's redirect URI.
//
// The redirect URI is the fallback key rather than the name because it is
// what actually has to match for the flow to work, and because GitLab
// permits several applications to share a name.
func findOAuthApplication(ctx context.Context, write oauthApplicationManager, rememberedClientID, redirectURI string) (*gitlabclient.OAuthApplication, error) {
	var byRedirectURI *gitlabclient.OAuthApplication

	for app, err := range write.ListOAuthApplications(gitlab.WithContext(ctx)) {
		if err != nil {
			return nil, fmt.Errorf("failed to look for an existing oauth application: %w", err)
		}

		if rememberedClientID != "" && app.ClientID == rememberedClientID {
			return app, nil
		}

		if byRedirectURI == nil && sameRedirectURI(app.CallbackURL, redirectURI) {
			byRedirectURI = app
		}
	}

	return byRedirectURI, nil
}

// sameRedirectURI compares two redirect URIs the way GitLab does for the
// purpose of matching this app's own registration: exactly, but tolerating
// the trailing whitespace and newline a hand-registered application picks
// up from being pasted into a form.
func sameRedirectURI(a, b string) bool {
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

// loadOAuthClientID reads the remembered client ID, returning "" when
// nothing has been remembered yet.
func loadOAuthClientID(ctx context.Context, conn *gorm.DB) (string, error) {
	var state db.SyncState

	err := conn.WithContext(ctx).Where("key = ?", oauthClientIDStateKey).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to load the remembered oauth client id: %w", err)
	}

	return state.Value, nil
}

// saveOAuthClientID remembers which application is this app's.
func saveOAuthClientID(ctx context.Context, conn *gorm.DB, clientID string) error {
	txn := conn.WithContext(ctx)

	var state db.SyncState

	err := txn.Where("key = ?", oauthClientIDStateKey).First(&state).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		err = txn.Create(&db.SyncState{Key: oauthClientIDStateKey, Value: clientID}).Error
	case err != nil:
		return fmt.Errorf("failed to load the remembered oauth client id: %w", err)
	default:
		state.Value = clientID
		err = txn.Save(&state).Error
	}

	if err != nil {
		return fmt.Errorf("failed to remember the oauth client id: %w", err)
	}

	return nil
}
