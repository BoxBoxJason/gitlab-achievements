package bootstrap

import (
	"errors"
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// readVerifier is the subset of gitlabclient.ReadClient permission
// verification needs.
type readVerifier interface {
	CurrentUser(options ...gitlab.RequestOptionFunc) (*gitlab.User, error)
	GetGroup(gid any, opt *gitlab.GetGroupOptions, options ...gitlab.RequestOptionFunc) (*gitlab.Group, error)
}

// writeVerifier is the subset of gitlabclient.WriteClient permission
// verification needs.
type writeVerifier interface {
	CurrentUser(options ...gitlab.RequestOptionFunc) (*gitlab.User, error)
	GetNamespaceMember(gid any, userID int64, options ...gitlab.RequestOptionFunc) (*gitlab.GroupMember, error)
}

// verifyPermissions checks that the read and write tokens actually have the
// access this app claims to need, and resolves namespacePath to its
// numeric namespace ID along the way:
//
//   - the read token must authenticate and be able to read the achievements
//     namespace
//   - the write token must authenticate, belong to an instance admin
//     (required to manage system hooks), and hold at least Maintainer on
//     the achievements namespace (required to create/update achievements)
//
// Every problem found is reported together, not just the first one, so an
// operator can fix a misconfiguration in a single pass.
func verifyPermissions(read readVerifier, write writeVerifier, namespacePath string) (int64, error) {
	var errs []error

	_, err := read.CurrentUser()
	if err != nil {
		errs = append(errs, fmt.Errorf("read token: %w", err))
	}

	namespace, err := read.GetGroup(namespacePath, nil)
	if err != nil {
		errs = append(errs, fmt.Errorf("read token: failed to read achievements namespace %q: %w", namespacePath, err))
	}

	writeUser, err := write.CurrentUser()
	if err != nil {
		errs = append(errs, fmt.Errorf("write token: %w", err))
	} else if !writeUser.IsAdmin {
		errs = append(errs, fmt.Errorf("write token: user %q is not an instance admin, required to manage system hooks", writeUser.Username))
	}

	if namespace != nil && writeUser != nil {
		member, err := write.GetNamespaceMember(namespace.ID, writeUser.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("write token: failed to verify role on namespace %q: %w", namespacePath, err))
		} else if member.AccessLevel < gitlab.MaintainerPermissions {
			errs = append(errs, fmt.Errorf(
				"write token: user %q has access level %d on namespace %q, need at least Maintainer (%d)",
				writeUser.Username, member.AccessLevel, namespacePath, gitlab.MaintainerPermissions,
			))
		}
	}

	joined := errors.Join(errs...)
	if joined != nil {
		return 0, joined
	}

	return namespace.ID, nil
}
