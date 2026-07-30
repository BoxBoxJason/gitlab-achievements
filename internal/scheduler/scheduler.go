// Package scheduler runs a function on a fixed interval until its context
// is cancelled. It backs this app's periodic reconciliation jobs (webhook
// and achievement/award healing); the intervals involved are fixed
// durations, not cron expressions, so a plain ticker is all that's needed.
package scheduler

import (
	"context"
	"time"
)

// Every calls task once per interval until ctx is cancelled. It does not
// call task immediately on start. Callers are expected to have already
// done an initial run (e.g. during application bootstrap) before scheduling
// the recurring one.
//
// An error returned by task is passed to onError rather than stopping the
// loop: a transient failure (a GitLab hiccup, a momentary DB blip) should
// be retried on the next tick, not take down the process.
func Every(ctx context.Context, interval time.Duration, task func(context.Context) error, onError func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := task(ctx)
			if err != nil && onError != nil {
				onError(err)
			}
		}
	}
}
