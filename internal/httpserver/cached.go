package httpserver

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// cachedCheckTimeout bounds the one check a window's callers share. It is
// cut loose from whichever request started it, so that request going away
// cannot fail the check for everyone waiting on it.
const cachedCheckTimeout = 5 * time.Second

// Cached wraps check so it runs at most once per ttl, however many callers
// ask: concurrent callers share one run, and every caller within ttl of it
// gets its answer, failure included.
//
// It exists for checks that cost something upstream. /readyz is served on
// the public listener without authentication, so an uncached check that
// calls GitLab with this app's read token lets anybody spend that token,
// and its client's rate limit, at whatever rate they can send requests. A
// ttl under the probe period leaves the orchestrator's own probes as fresh
// as before.
func Cached(check Checker, ttl time.Duration) Checker {
	cached := &cachedChecker{check: check, ttl: ttl, now: time.Now}

	return cached.run
}

type cachedChecker struct {
	checkedAt time.Time
	err       error
	check     Checker
	now       func() time.Time
	group     singleflight.Group
	ttl       time.Duration
	mu        sync.Mutex
	checked   bool
}

func (c *cachedChecker) run(ctx context.Context) error {
	if err, ok := c.recent(); ok {
		return err
	}

	result := c.group.DoChan("", func() (any, error) {
		if err, ok := c.recent(); ok {
			return nil, err
		}

		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cachedCheckTimeout)
		defer cancel()

		err := c.check(checkCtx)

		c.mu.Lock()
		c.checkedAt, c.err, c.checked = c.now(), err, true
		c.mu.Unlock()

		return nil, err
	})

	select {
	case shared := <-result:
		return shared.Err
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the caller's own cancellation, reported as is
	}
}

// recent returns the last answer when it is younger than ttl.
func (c *cachedChecker) recent() (error, bool) { //nolint:revive // the error is the cached answer, not this call's failure
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.checked || c.now().Sub(c.checkedAt) >= c.ttl {
		return nil, false
	}

	return c.err, true
}
