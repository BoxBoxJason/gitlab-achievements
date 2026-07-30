package bootstrap

import (
	"errors"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

type fakeHookManager struct {
	hooks     []*gitlab.Hook
	listErr   error
	getErr    error
	addErr    error
	editErr   error
	addCalls  int
	editCalls int
	getCalls  int
	listCalls int
	nextID    int64
}

func (f *fakeHookManager) ListSystemHooks(...gitlab.RequestOptionFunc) ([]*gitlab.Hook, error) {
	f.listCalls++

	if f.listErr != nil {
		return nil, f.listErr
	}

	return f.hooks, nil
}

// GetSystemHook simulates the real GitLab behavior: a 404 (ErrNotFound) for
// any ID not present in f.hooks, so tests can simulate an out-of-band
// deletion just by leaving the hook out of the fixture.
func (f *fakeHookManager) GetSystemHook(hook int64, _ ...gitlab.RequestOptionFunc) (*gitlab.Hook, error) {
	f.getCalls++

	if f.getErr != nil {
		return nil, f.getErr
	}

	for _, h := range f.hooks {
		if h.ID == hook {
			return h, nil
		}
	}

	return nil, gitlab.ErrNotFound
}

func (f *fakeHookManager) AddSystemHook(opt *gitlab.AddHookOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.Hook, error) {
	f.addCalls++

	if f.addErr != nil {
		return nil, f.addErr
	}

	f.nextID++

	return &gitlab.Hook{ID: f.nextID, URL: *opt.URL}, nil
}

func (f *fakeHookManager) EditSystemHook(hook int64, opt *gitlab.EditHookOptions, _ ...gitlab.RequestOptionFunc) (*gitlab.Hook, error) {
	f.editCalls++

	if f.editErr != nil {
		return nil, f.editErr
	}

	return &gitlab.Hook{ID: hook, URL: *opt.URL}, nil
}

const testWebhookURL = "https://achievements.example.com/webhooks/system"

func TestSyncSystemHook_CreatesWhenMissing(t *testing.T) {
	write := &fakeHookManager{}
	conn := testConn(t)

	report, err := syncSystemHook(write, conn, testWebhookURL, "s3cr3t")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !report.Created {
		t.Errorf("expected Created to be true")
	}

	if write.addCalls != 1 || write.editCalls != 0 {
		t.Errorf("expected exactly one AddSystemHook call, got add=%d edit=%d", write.addCalls, write.editCalls)
	}

	storedID, found, err := loadWebhookID(conn)
	if err != nil || !found {
		t.Fatalf("expected the new hook id to be persisted, found=%v err=%v", found, err)
	}

	if storedID != report.HookID {
		t.Errorf("expected stored id %d to match report %d", storedID, report.HookID)
	}
}

func TestSyncSystemHook_ReusesMatchingURL(t *testing.T) {
	write := &fakeHookManager{
		hooks: []*gitlab.Hook{
			{ID: 5, URL: testWebhookURL},
		},
	}
	conn := testConn(t)

	report, err := syncSystemHook(write, conn, testWebhookURL, "s3cr3t")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created {
		t.Errorf("expected Created to be false when a matching hook already exists")
	}

	if report.HookID != 5 {
		t.Errorf("expected HookID 5, got %d", report.HookID)
	}

	if write.editCalls != 1 || write.addCalls != 0 {
		t.Errorf("expected exactly one EditSystemHook call, got add=%d edit=%d", write.addCalls, write.editCalls)
	}

	storedID, found, err := loadWebhookID(conn)
	if err != nil || !found || storedID != 5 {
		t.Errorf("expected id 5 to be persisted, got %d found=%v err=%v", storedID, found, err)
	}
}

func TestSyncSystemHook_IgnoresNonMatchingHooks(t *testing.T) {
	write := &fakeHookManager{
		hooks: []*gitlab.Hook{
			{ID: 1, URL: "https://someone-else.example.com/hook"},
		},
	}

	report, err := syncSystemHook(write, testConn(t), testWebhookURL, "s3cr3t")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !report.Created {
		t.Errorf("expected a new hook to be created since no existing hook matched the URL")
	}
}

func TestSyncSystemHook_ListError(t *testing.T) {
	write := &fakeHookManager{listErr: errors.New("403 forbidden")}

	_, err := syncSystemHook(write, testConn(t), testWebhookURL, "s3cr3t")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestSyncSystemHook_AddError(t *testing.T) {
	write := &fakeHookManager{addErr: errors.New("403 forbidden")}

	_, err := syncSystemHook(write, testConn(t), testWebhookURL, "s3cr3t")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestSyncSystemHook_UsesStoredIDDirectly(t *testing.T) {
	conn := testConn(t)
	write := &fakeHookManager{
		hooks: []*gitlab.Hook{{ID: 7, URL: testWebhookURL}},
	}

	if err := storeWebhookID(conn, 7); err != nil {
		t.Fatalf("failed to seed stored webhook id: %v", err)
	}

	report, err := syncSystemHook(write, conn, testWebhookURL, "s3cr3t")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if report.Created || report.HookID != 7 {
		t.Errorf("expected the stored hook to be reused, got %+v", report)
	}

	if write.getCalls != 1 {
		t.Errorf("expected exactly one GetSystemHook call, got %d", write.getCalls)
	}

	if write.listCalls != 0 {
		t.Errorf("expected no ListSystemHooks scan when a stored id resolves directly, got %d calls", write.listCalls)
	}

	if write.editCalls != 1 {
		t.Errorf("expected the resolved hook to be edited to heal any drift, got %d calls", write.editCalls)
	}
}

func TestSyncSystemHook_RecreatesWhenStoredHookDeleted(t *testing.T) {
	conn := testConn(t)
	write := &fakeHookManager{} // no hooks: GetSystemHook(7) and ListSystemHooks both come back empty

	if err := storeWebhookID(conn, 7); err != nil {
		t.Fatalf("failed to seed stored webhook id: %v", err)
	}

	report, err := syncSystemHook(write, conn, testWebhookURL, "s3cr3t")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !report.Created {
		t.Errorf("expected a new hook to be created after the stored one was confirmed deleted")
	}

	if write.getCalls != 1 || write.listCalls != 1 || write.addCalls != 1 {
		t.Errorf("expected get=1 list=1 add=1, got get=%d list=%d add=%d", write.getCalls, write.listCalls, write.addCalls)
	}

	storedID, found, err := loadWebhookID(conn)
	if err != nil || !found || storedID != report.HookID {
		t.Errorf("expected the recreated hook id %d to overwrite the stale one, got %d found=%v err=%v", report.HookID, storedID, found, err)
	}
}

func TestSyncSystemHook_TransientGetErrorIsNotTreatedAsDeleted(t *testing.T) {
	conn := testConn(t)
	write := &fakeHookManager{getErr: errors.New("500 internal server error")}

	if err := storeWebhookID(conn, 7); err != nil {
		t.Fatalf("failed to seed stored webhook id: %v", err)
	}

	_, err := syncSystemHook(write, conn, testWebhookURL, "s3cr3t")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if write.listCalls != 0 || write.addCalls != 0 {
		t.Errorf("expected no fallback scan/create on a transient error, got list=%d add=%d", write.listCalls, write.addCalls)
	}
}

func TestReconcileWebhook_DelegatesToSyncSystemHook(t *testing.T) {
	write := &fakeHookManager{}

	report, err := ReconcileWebhook(write, testConn(t), testWebhookURL, "s3cr3t")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !report.Created || write.addCalls != 1 {
		t.Errorf("expected ReconcileWebhook to behave exactly like syncSystemHook, got %+v add=%d", report, write.addCalls)
	}
}
