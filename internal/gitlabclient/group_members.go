package gitlabclient

import (
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
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
