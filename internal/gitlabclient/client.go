// Package gitlabclient talks to a self-hosted GitLab instance over its REST
// and GraphQL APIs.
//
// It is split into two client types so that the split is enforced by the
// compiler rather than by convention:
//
//   - ReadClient wraps a read_api-scoped token and only exposes Get/List
//     operations (users, groups, projects, commits, merge requests, issues,
//     pipelines, events).
//   - WriteClient wraps an api-scoped token, expected to belong to a service
//     account whose GitLab role/membership is restricted separately, and
//     only exposes the mutations this project needs: managing system hooks
//     and awarding/revoking achievements.
//
// Neither type embeds the underlying *gitlab.Client, so callers cannot reach
// past the curated method set to call an operation the token isn't meant
// to be used for.
//
// Retries on 429/5xx responses (honoring the Retry-After header) are
// handled internally by the underlying HTTP client and require no extra
// configuration; see gitlab.ClientOptionFunc if that behavior needs
// tuning (e.g. gitlab.WithoutRetries).
package gitlabclient

import (
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// ReadClient talks to GitLab using a read_api-scoped token.
type ReadClient struct {
	raw *gitlab.Client
}

// WriteClient talks to GitLab using an api-scoped token.
type WriteClient struct {
	raw *gitlab.Client
}

// NewReadClient builds a ReadClient for the GitLab instance at baseURL,
// authenticating with token.
func NewReadClient(baseURL, token string, opts ...gitlab.ClientOptionFunc) (*ReadClient, error) {
	raw, err := newRawClient(baseURL, token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to build gitlab read client: %w", err)
	}

	return &ReadClient{raw: raw}, nil
}

// NewWriteClient builds a WriteClient for the GitLab instance at baseURL,
// authenticating with token.
func NewWriteClient(baseURL, token string, opts ...gitlab.ClientOptionFunc) (*WriteClient, error) {
	raw, err := newRawClient(baseURL, token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to build gitlab write client: %w", err)
	}

	return &WriteClient{raw: raw}, nil
}

func newRawClient(baseURL, token string, opts ...gitlab.ClientOptionFunc) (*gitlab.Client, error) {
	allOpts := make([]gitlab.ClientOptionFunc, 0, len(opts)+1)
	allOpts = append(allOpts, gitlab.WithBaseURL(baseURL))
	allOpts = append(allOpts, opts...)

	raw, err := gitlab.NewClient(token, allOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create gitlab client: %w", err)
	}

	return raw, nil
}
