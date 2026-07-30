package gitlabclient

import (
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// GetLicense retrieves the instance's license, whose plan decides which
// webhook strategy this app can use (group hooks are Premium/Ultimate only).
//
// It lives on WriteClient rather than ReadClient because /license is an
// admin-only endpoint, and the write token is the one already required to
// belong to an instance admin.
//
// A GitLab Community Edition instance has no such endpoint at all, and an
// unlicensed Enterprise Edition instance has no license to return: both
// answer 404, which callers are expected to read as "free tier" rather than
// as a failure.
func (c *WriteClient) GetLicense(options ...gitlab.RequestOptionFunc) (*gitlab.License, error) {
	license, _, err := c.raw.License.GetLicense(options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance license: %w", err)
	}

	return license, nil
}
