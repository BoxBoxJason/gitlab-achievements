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
//     only exposes the mutations this project needs: managing the group and
//     project webhooks that feed event ingestion, and awarding/revoking
//     achievements.
//
// Neither type embeds the underlying *gitlab.Client, so callers cannot reach
// past the curated method set to call an operation the token isn't meant
// to be used for.
//
// Retries on 429/5xx responses (honoring the Retry-After header) are
// handled internally by the underlying HTTP client and require no extra
// configuration; see gitlab.ClientOptionFunc if that behavior needs
// tuning (e.g. gitlab.WithoutRetries). WriteClient only retries a 5xx for
// an idempotent method (see NewWriteClient).
package gitlabclient

import (
	"fmt"
	"net/http"

	"github.com/hashicorp/go-cleanhttp"
	gitlab "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/boxboxjason/gitlab-achievements/internal/metrics"
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
	raw, err := newRawClient(baseURL, token, "read", opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to build gitlab read client: %w", err)
	}

	return &ReadClient{raw: raw}, nil
}

// NewWriteClient builds a WriteClient for the GitLab instance at baseURL,
// authenticating with token.
//
// Its retries are limited to idempotent methods. Every GraphQL mutation is a
// POST, and a 5xx can arrive after GitLab has already committed one (a
// gateway timing out on a slow request, a worker restarting before it
// answered). Replaying achievementsAward then creates a second award and a
// second email, and the pass that made the call only ever records the last
// one, so the rest stay on the recipient's profile for good. A 5xx is
// instead returned to the caller, which re-reads what GitLab holds before
// acting again. A 429 is still retried for every method, since GitLab
// rejects a rate-limited request before acting on it.
func NewWriteClient(baseURL, token string, opts ...gitlab.ClientOptionFunc) (*WriteClient, error) {
	opts = append([]gitlab.ClientOptionFunc{gitlab.WithOnlyIdempotentRetries()}, opts...)

	raw, err := newRawClient(baseURL, token, "write", opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to build gitlab write client: %w", err)
	}

	return &WriteClient{raw: raw}, nil
}

// newRawClient builds the underlying client, its requests counted under
// name in the GitLab request metrics.
//
// The instrumented HTTP client goes first, so a caller passing its own with
// gitlab.WithHTTPClient (tests do) still replaces it. It is the same pooled
// client the library would otherwise build for itself, wrapped.
func newRawClient(baseURL, token, name string, opts ...gitlab.ClientOptionFunc) (*gitlab.Client, error) {
	defaults := []gitlab.ClientOptionFunc{
		gitlab.WithHTTPClient(&http.Client{Transport: metrics.Transport(name, cleanhttp.DefaultPooledTransport())}),
		gitlab.WithBaseURL(baseURL),
	}

	allOpts := make([]gitlab.ClientOptionFunc, 0, len(defaults)+len(opts))
	allOpts = append(allOpts, defaults...)
	allOpts = append(allOpts, opts...)

	raw, err := gitlab.NewClient(token, allOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create gitlab client: %w", err)
	}

	return raw, nil
}
