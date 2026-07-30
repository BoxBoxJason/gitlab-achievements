package gitlabclient

import (
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"golang.org/x/time/rate"
)

// WithRateLimit caps how fast a client issues requests to GitLab, blocking
// (until the request's context expires) rather than failing when the cap is
// reached.
//
// This is pacing, not error handling: retrying a 429 with the Retry-After
// delay is already handled inside client-go for every client (see this
// package's doc comment). What a cap adds is not sending the request that
// would have earned the 429 in the first place, which matters for the
// historical backfill, the only workload here that reads at a rate a GitLab
// instance would notice.
//
// Passing it also replaces client-go's default limiter, which derives its
// rate from the instance's RateLimit-* response headers. That default aims
// to use as much of the instance's budget as it is allowed to; an explicit
// cap deliberately leaves headroom for everything else using the API.
//
// burst is clamped to at least 1, since a limiter that permits no burst at
// all can never admit a request.
func WithRateLimit(perSecond float64, burst int) gitlab.ClientOptionFunc {
	return gitlab.WithCustomLimiter(rate.NewLimiter(rate.Limit(perSecond), max(burst, 1)))
}
