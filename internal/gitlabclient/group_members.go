package gitlabclient

import (
	"context"
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
)

// GetNamespaceMember retrieves userID's membership of the group identified
// by gid (numeric ID or full path), including access inherited from parent
// groups. It is used during bootstrap to verify the write token's account
// holds at least Maintainer on the configured achievements namespace.
func (c *WriteClient) GetNamespaceMember(gid any, userID int64, options ...gitlab.RequestOptionFunc) (*gitlab.GroupMember, error) {
	member, _, err := c.raw.GroupMembers.GetInheritedGroupMember(gid, userID, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace member: %w", err)
	}

	return member, nil
}

// IsGroupMember reports whether a user belongs to group (an ID or full
// path), directly or through a parent group. A user GitLab reports no
// membership for is a plain false, not an error.
func (c *ReadClient) IsGroupMember(ctx context.Context, group string, gitlabUserID int64) (bool, error) {
	_, _, err := c.raw.GroupMembers.GetInheritedGroupMember(group, gitlabUserID, gitlab.WithContext(ctx))
	if IsNotFound(err) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("failed to check membership of %q: %w", group, err)
	}

	return true, nil
}
