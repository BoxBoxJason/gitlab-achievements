package gitlabclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// IsNotFound reports whether err represents a 404 response from GitLab.
func IsNotFound(err error) bool {
	return errors.Is(err, gitlab.ErrNotFound)
}

// IsPermissionError reports whether err represents an authentication or
// authorization failure (401/403): the token is missing, invalid, expired,
// or lacks the scope/role required for the call. These are not worth
// retrying. They need operator intervention (rotate the token, fix its
// role) rather than a backoff.
func IsPermissionError(err error) bool {
	return gitlab.HasStatusCode(err, http.StatusUnauthorized) ||
		gitlab.HasStatusCode(err, http.StatusForbidden)
}

// IsTransient reports whether err is likely to succeed if the call is
// retried later: a 429 or 5xx response, or a network-level failure that
// never reached GitLab (timeout, connection reset, DNS failure).
//
// Note that client-go already retries 429/5xx responses internally with
// backoff (honoring Retry-After); IsTransient surfaces after those retries
// are exhausted, and is meant for a caller-level decision such as
// rescheduling a background job.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}

	if errResp, ok := errors.AsType[*gitlab.ErrorResponse](err); ok {
		return errResp.StatusCode == http.StatusTooManyRequests || errResp.StatusCode >= http.StatusInternalServerError
	}

	if errors.Is(err, gitlab.ErrNotFound) {
		return false
	}

	if errors.Is(err, context.Canceled) {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	_, ok := errors.AsType[*url.Error](err)

	return ok
}

// MutationError wraps the user-facing messages returned inside a GraphQL
// mutation payload's `errors` field. GitLab returns these alongside a 200
// OK response, a permission problem such as "you don't have permission to
// award this achievement" surfaces here, not as an IsPermissionError
// transport failure.
type MutationError struct {
	Mutation string
	Messages []string
}

func (e *MutationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Mutation, strings.Join(e.Messages, "; "))
}
