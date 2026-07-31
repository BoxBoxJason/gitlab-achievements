package bootstrap

import (
	"errors"
	"iter"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	appdb "github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

const testRedirectURI = "https://achievements.example.com/oauth/callback"

// fakeApplications is a stand-in instance holding a set of OAuth
// applications.
type fakeApplications struct {
	existing  []*gitlabclient.OAuthApplication
	listErr   error
	createErr error
	created   int
	nextID    string
}

func (f *fakeApplications) ListOAuthApplications(...gitlab.RequestOptionFunc) iter.Seq2[*gitlabclient.OAuthApplication, error] {
	return func(yield func(*gitlabclient.OAuthApplication, error) bool) {
		if f.listErr != nil {
			yield(nil, f.listErr)

			return
		}

		for _, app := range f.existing {
			if !yield(app, nil) {
				return
			}
		}
	}
}

func (f *fakeApplications) CreateOAuthApplication(name, redirectURI, _ string, confidential bool, _ ...gitlab.RequestOptionFunc) (*gitlabclient.OAuthApplication, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}

	f.created++

	id := f.nextID
	if id == "" {
		id = "created-client-id"
	}

	app := &gitlabclient.OAuthApplication{
		ClientID:     id,
		Name:         name,
		CallbackURL:  redirectURI,
		Confidential: confidential,
	}
	f.existing = append(f.existing, app)

	return app, nil
}

func oauthTestConn(t *testing.T) *gorm.DB {
	t.Helper()

	conn, err := appdb.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory test database: %v", err)
	}

	if err := appdb.Migrate(conn); err != nil {
		t.Fatalf("failed to migrate test database: %v", err)
	}

	return conn
}

func TestEnsureOAuthApplication_RegistersOneOnFirstRun(t *testing.T) {
	conn := oauthTestConn(t)
	instance := &fakeApplications{}

	clientID, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if clientID != "created-client-id" {
		t.Errorf("unexpected client id %q", clientID)
	}

	if instance.created != 1 {
		t.Errorf("expected one application created, got %d", instance.created)
	}
}

// GitLab returns a client secret once and never again, so the application
// this app registers for itself has to be one that doesn't have one.
func TestEnsureOAuthApplication_RegistersAPublicClient(t *testing.T) {
	conn := oauthTestConn(t)
	instance := &fakeApplications{}

	if _, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop()); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if instance.existing[0].Confidential {
		t.Error("expected a public (non-confidential) application")
	}
}

// The whole point of remembering the client ID is that a restart adopts
// the existing application rather than littering the instance with one per
// boot.
func TestEnsureOAuthApplication_AdoptsTheRememberedApplicationOnRestart(t *testing.T) {
	conn := oauthTestConn(t)
	instance := &fakeApplications{}

	first, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	second, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if first != second {
		t.Errorf("expected the same application to be adopted, got %q then %q", first, second)
	}

	if instance.created != 1 {
		t.Errorf("expected no second application to be created, got %d", instance.created)
	}
}

// A state row lost (restored database, wiped volume) while the application
// survives on GitLab must not produce a duplicate.
func TestEnsureOAuthApplication_AdoptsByRedirectURIWhenTheStateRowIsGone(t *testing.T) {
	conn := oauthTestConn(t)
	instance := &fakeApplications{
		existing: []*gitlabclient.OAuthApplication{
			{ClientID: "already-there", CallbackURL: testRedirectURI},
		},
	}

	clientID, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if clientID != "already-there" {
		t.Errorf("expected the existing application to be adopted, got %q", clientID)
	}

	if instance.created != 0 {
		t.Error("expected no application to be created")
	}

	// ...and the adoption should be remembered, so the next start takes the
	// cheaper path.
	remembered, err := loadOAuthClientID(t.Context(), conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if remembered != "already-there" {
		t.Errorf("expected the adopted client id to be remembered, got %q", remembered)
	}
}

// An application registered against somebody else's callback URL is not
// this app's, however similar it looks.
func TestEnsureOAuthApplication_IgnoresApplicationsForOtherRedirectURIs(t *testing.T) {
	conn := oauthTestConn(t)
	instance := &fakeApplications{
		existing: []*gitlabclient.OAuthApplication{
			{ClientID: "someone-elses", CallbackURL: "https://other.example.com/oauth/callback"},
		},
	}

	clientID, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if clientID == "someone-elses" {
		t.Error("expected an unrelated application not to be adopted")
	}

	if instance.created != 1 {
		t.Errorf("expected a new application to be created, got %d", instance.created)
	}
}

// A remembered application deleted out of band should be replaced, not
// fail every login from then on.
func TestEnsureOAuthApplication_RegistersAgainWhenTheRememberedOneIsGone(t *testing.T) {
	conn := oauthTestConn(t)
	instance := &fakeApplications{}

	if _, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop()); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// The admin deleted it on GitLab; the state row still names it.
	instance.existing = nil
	instance.nextID = "second-client-id"

	clientID, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if clientID != "second-client-id" {
		t.Errorf("expected a replacement to be registered, got %q", clientID)
	}

	remembered, err := loadOAuthClientID(t.Context(), conn)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if remembered != "second-client-id" {
		t.Errorf("expected the replacement to be remembered, got %q", remembered)
	}
}

func TestEnsureOAuthApplication_PropagatesAListFailure(t *testing.T) {
	conn := oauthTestConn(t)
	instance := &fakeApplications{listErr: errors.New("403 forbidden")}

	if _, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop()); err == nil {
		t.Error("expected the list failure to be reported")
	}
}

func TestEnsureOAuthApplication_PropagatesACreateFailure(t *testing.T) {
	conn := oauthTestConn(t)
	instance := &fakeApplications{createErr: errors.New("403 forbidden")}

	if _, err := EnsureOAuthApplication(t.Context(), instance, conn, testRedirectURI, zap.NewNop()); err == nil {
		t.Error("expected the create failure to be reported")
	}
}
