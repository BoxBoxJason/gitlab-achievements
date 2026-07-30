package bootstrap

import (
	"errors"
	"strings"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

type fakeReadVerifier struct {
	currentUserErr error
	group          *gitlab.Group
	getGroupErr    error
}

func (f *fakeReadVerifier) CurrentUser(...gitlab.RequestOptionFunc) (*gitlab.User, error) {
	if f.currentUserErr != nil {
		return nil, f.currentUserErr
	}

	return &gitlab.User{ID: 1, Username: "reader"}, nil
}

func (f *fakeReadVerifier) GetGroup(any, *gitlab.GetGroupOptions, ...gitlab.RequestOptionFunc) (*gitlab.Group, error) {
	if f.getGroupErr != nil {
		return nil, f.getGroupErr
	}

	return f.group, nil
}

type fakeWriteVerifier struct {
	user           *gitlab.User
	currentUserErr error
	member         *gitlab.GroupMember
	getMemberErr   error
}

func (f *fakeWriteVerifier) CurrentUser(...gitlab.RequestOptionFunc) (*gitlab.User, error) {
	if f.currentUserErr != nil {
		return nil, f.currentUserErr
	}

	return f.user, nil
}

func (f *fakeWriteVerifier) GetNamespaceMember(any, int64, ...gitlab.RequestOptionFunc) (*gitlab.GroupMember, error) {
	if f.getMemberErr != nil {
		return nil, f.getMemberErr
	}

	return f.member, nil
}

func validReadVerifier() *fakeReadVerifier {
	return &fakeReadVerifier{group: &gitlab.Group{ID: 42, FullPath: "achievements"}}
}

func validWriteVerifier() *fakeWriteVerifier {
	return &fakeWriteVerifier{
		user:   &gitlab.User{ID: 7, Username: "bot", IsAdmin: true},
		member: &gitlab.GroupMember{AccessLevel: gitlab.OwnerPermissions},
	}
}

func TestVerifyPermissions_Valid(t *testing.T) {
	namespaceID, err := verifyPermissions(t.Context(), validReadVerifier(), validWriteVerifier(), "achievements")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if namespaceID != 42 {
		t.Errorf("expected namespace id 42, got %d", namespaceID)
	}
}

func TestVerifyPermissions_ReadTokenInvalid(t *testing.T) {
	read := validReadVerifier()
	read.currentUserErr = errors.New("401 unauthorized")

	_, err := verifyPermissions(t.Context(), read, validWriteVerifier(), "achievements")
	if err == nil || !strings.Contains(err.Error(), "read token") {
		t.Fatalf("expected a read token error, got: %v", err)
	}
}

func TestVerifyPermissions_ReadTokenCannotReadNamespace(t *testing.T) {
	read := validReadVerifier()
	read.getGroupErr = errors.New("404 not found")

	_, err := verifyPermissions(t.Context(), read, validWriteVerifier(), "achievements")
	if err == nil || !strings.Contains(err.Error(), "failed to read achievements namespace") {
		t.Fatalf("expected a namespace read error, got: %v", err)
	}
}

func TestVerifyPermissions_WriteTokenInvalid(t *testing.T) {
	write := validWriteVerifier()
	write.currentUserErr = errors.New("401 unauthorized")

	_, err := verifyPermissions(t.Context(), validReadVerifier(), write, "achievements")
	if err == nil || !strings.Contains(err.Error(), "write token") {
		t.Fatalf("expected a write token error, got: %v", err)
	}
}

func TestVerifyPermissions_WriteTokenNotAdmin(t *testing.T) {
	write := validWriteVerifier()
	write.user = &gitlab.User{ID: 7, Username: "bot", IsAdmin: false}

	_, err := verifyPermissions(t.Context(), validReadVerifier(), write, "achievements")
	if err == nil || !strings.Contains(err.Error(), "not an instance admin") {
		t.Fatalf("expected an admin error, got: %v", err)
	}
}

func TestVerifyPermissions_WriteTokenInsufficientNamespaceRole(t *testing.T) {
	write := validWriteVerifier()
	write.member = &gitlab.GroupMember{AccessLevel: gitlab.DeveloperPermissions}

	_, err := verifyPermissions(t.Context(), validReadVerifier(), write, "achievements")
	if err == nil || !strings.Contains(err.Error(), "need at least Maintainer") {
		t.Fatalf("expected an insufficient role error, got: %v", err)
	}
}

func TestVerifyPermissions_AggregatesAllErrors(t *testing.T) {
	read := validReadVerifier()
	read.currentUserErr = errors.New("read broken")

	write := validWriteVerifier()
	write.currentUserErr = errors.New("write broken")

	_, err := verifyPermissions(t.Context(), read, write, "achievements")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	if !strings.Contains(err.Error(), "read broken") || !strings.Contains(err.Error(), "write broken") {
		t.Errorf("expected aggregated error to mention both failures, got: %v", err)
	}
}
