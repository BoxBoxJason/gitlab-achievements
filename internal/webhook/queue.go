package webhook

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
)

const (
	// DefaultQueueSize is how many normalized activities may be waiting to
	// be evaluated before the receiver starts pushing back on GitLab. It is
	// deliberately generous: the queue's job is to absorb a burst (a CI
	// fleet finishing at once, a monorepo push) without making GitLab wait
	// on the database.
	DefaultQueueSize = 2048
	// DefaultWorkers is how many activities are evaluated concurrently. The
	// engine serializes each event in its own transaction, so this trades
	// database connections for throughput rather than unlocking parallelism
	// within one event.
	DefaultWorkers = 4
	// DefaultMaxAttempts is how many times an activity is evaluated before
	// the failure is reported and the activity dropped from the queue.
	DefaultMaxAttempts = 4
	// DefaultRetryBackoff is the delay before the second attempt; it doubles
	// with each further one.
	DefaultRetryBackoff = 250 * time.Millisecond
	// enqueueTimeout bounds how long a delivery waits for queue space
	// before the receiver rejects it. GitLab gives a webhook 10 seconds
	// before recording it as failed, so this has to leave room for the
	// response to get back.
	enqueueTimeout = 5 * time.Second
)

// ErrQueueFull reports that a delivery could not be accepted because
// evaluation is not keeping up. The receiver turns it into a 503, which is
// the honest answer: the event was not taken responsibility for, so GitLab
// should be told the delivery failed rather than have it silently dropped.
var ErrQueueFull = errors.New("activity queue is full")

// ErrShuttingDown reports that a delivery arrived after the queue stopped
// accepting work. It is also a 503: the process is going away and the
// delivery belongs to whichever instance is still serving.
var ErrShuttingDown = errors.New("activity queue is shutting down")

// Options tunes a Queue. The zero value selects the defaults above.
type Options struct {
	// Logger receives processing failures. May be nil.
	Logger *zap.Logger
	// Size is the queue's capacity in activities.
	Size int
	// Workers is how many activities are evaluated concurrently.
	Workers int
	// MaxAttempts is how many times one activity is tried.
	MaxAttempts int
	// RetryBackoff is the delay before the second attempt, doubling after.
	RetryBackoff time.Duration
}

// Queue decouples accepting a webhook delivery from evaluating it.
//
// GitLab records a webhook as failed if the response is slow, and disables
// hooks that keep failing, so the receiver must answer long before the
// achievement engine has finished with the event. Normalized activities are
// therefore handed to this queue and evaluated by background workers, with
// the HTTP response reporting only whether the activity was accepted.
//
// Losing the queue's contents on a crash is survivable but not silent: an
// activity that cannot be evaluated is retried, and reported at error with
// its dedup key if it still fails, so it can be replayed rather than
// quietly disappearing. Activities are idempotent by dedup key
// (activity.Processor's contract), so replaying is always safe.
type Queue struct {
	processor activity.Processor
	events    chan activity.Event
	logger    *zap.Logger
	// done is closed by Shutdown to stop accepting work. The events channel
	// is deliberately never closed: deliveries can still be in flight when
	// shutdown begins, and a send on a closed channel would panic the
	// server rather than fail the request.
	done chan struct{}
	// stopWork ends the workers' context, and is called only once Shutdown
	// has drained them (or given up waiting). Written by Start, read by
	// Shutdown, which the Start-then-Shutdown contract orders.
	stopWork  context.CancelFunc
	opts      Options
	wg        sync.WaitGroup
	accepted  atomic.Int64
	processed atomic.Int64
	failed    atomic.Int64
	rejected  atomic.Int64
	closeOnce sync.Once
}

// QueueStats summarizes what a Queue has handled since it was built.
type QueueStats struct {
	// Accepted counts activities taken onto the queue.
	Accepted int64
	// Processed counts activities the engine evaluated successfully.
	Processed int64
	// Failed counts activities dropped after exhausting their attempts.
	Failed int64
	// Rejected counts activities refused because the queue was full.
	Rejected int64
	// Pending counts activities waiting to be evaluated right now.
	Pending int
}

// NewQueue builds a Queue feeding processor. Call Start to begin draining
// it.
func NewQueue(processor activity.Processor, opts Options) *Queue {
	opts.applyDefaults()

	return &Queue{
		processor: processor,
		events:    make(chan activity.Event, opts.Size),
		logger:    loggerOrNop(opts.Logger),
		done:      make(chan struct{}),
		opts:      opts,
	}
}

func (o *Options) applyDefaults() {
	if o.Size <= 0 {
		o.Size = DefaultQueueSize
	}

	if o.Workers <= 0 {
		o.Workers = DefaultWorkers
	}

	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}

	if o.RetryBackoff <= 0 {
		o.RetryBackoff = DefaultRetryBackoff
	}
}

// Start launches the workers that drain the queue. They stop once Shutdown
// has closed the queue and everything already accepted has been evaluated.
//
// ctx supplies the workers' values, and deliberately not their lifetime
// (see context.WithoutCancel). A Queue is ended by Shutdown, which drains
// what it accepted before stopping the workers, and callers overwhelmingly
// have only one context to hand: the one cancelled at SIGTERM. Honoring
// that cancellation here would have the workers return the instant the
// signal arrived, with the queue still full, and Shutdown would then find
// nothing left to wait for and report success while discarding activity
// GitLab was already told succeeded. Whichever context a caller passes,
// only Shutdown decides when the draining stops.
func (q *Queue) Start(ctx context.Context) {
	workCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	q.stopWork = stop

	for range q.opts.Workers {
		q.wg.Go(func() {
			q.work(workCtx)
		})
	}
}

// Enqueue takes responsibility for evaluating events, waiting briefly for
// space if the queue is full.
//
// Waiting rather than failing immediately is what lets a burst of
// deliveries ride out a slow moment in the database instead of being
// rejected outright; the wait is bounded so a genuinely stuck engine
// surfaces as a rejected delivery rather than a request GitLab times out.
//
// Events are enqueued one by one and may interleave with other deliveries'.
// That is deliberate: each carries its own dedup key and its own counter
// updates, so nothing about a payload's several activities requires them to
// be evaluated together or in order.
func (q *Queue) Enqueue(ctx context.Context, events []activity.Event) error {
	if len(events) == 0 {
		return nil
	}

	timer := time.NewTimer(enqueueTimeout)
	defer timer.Stop()

	for index, event := range events {
		// Checked ahead of the send rather than only alongside it: once
		// shutdown has begun nothing drains the queue any further, so an
		// activity accepted into the remaining buffer space would be lost
		// after GitLab was told the delivery succeeded. A select with both
		// cases ready picks between them at random, which is exactly the
		// wrong coin to flip here.
		select {
		case <-q.done:
			q.rejected.Add(int64(len(events) - index))

			return ErrShuttingDown
		default:
		}

		select {
		case q.events <- event:
			q.accepted.Add(1)
		case <-q.done:
			q.rejected.Add(int64(len(events) - index))

			return ErrShuttingDown
		case <-ctx.Done():
			q.rejected.Add(int64(len(events) - index))

			return fmt.Errorf("delivery abandoned before it could be queued: %w", ctx.Err())
		case <-timer.C:
			q.rejected.Add(int64(len(events) - index))

			return ErrQueueFull
		}
	}

	return nil
}

// Shutdown stops accepting new activities and waits for those already
// accepted to be evaluated, returning early if ctx expires first.
//
// Draining rather than dropping is what keeps a rolling restart from losing
// the deliveries GitLab has already been told succeeded. The workers'
// context is cancelled only once that wait is over, either way: an
// evaluation still running when ctx expires is abandoned rather than left
// holding a database transaction open past the deadline the caller set.
func (q *Queue) Shutdown(ctx context.Context) error {
	q.closeOnce.Do(func() {
		close(q.done)
	})

	drained := make(chan struct{})

	go func() {
		q.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		q.stopWorkers()

		return nil
	case <-ctx.Done():
		q.stopWorkers()

		return fmt.Errorf("timed out draining queued activity: %w", ctx.Err())
	}
}

// Stats returns a snapshot of the queue's counters.
func (q *Queue) Stats() QueueStats {
	return QueueStats{
		Accepted:  q.accepted.Load(),
		Processed: q.processed.Load(),
		Failed:    q.failed.Load(),
		Rejected:  q.rejected.Load(),
		Pending:   len(q.events),
	}
}

// stopWorkers ends the workers' context, tolerating a Queue that was never
// started so Shutdown stays safe to call unconditionally.
func (q *Queue) stopWorkers() {
	if q.stopWork != nil {
		q.stopWork()
	}
}

// work evaluates activities until shutdown is signalled and the queue has
// been emptied, or ctx is cancelled.
func (q *Queue) work(ctx context.Context) {
	for {
		select {
		case event := <-q.events:
			q.evaluate(ctx, event)
		case <-q.done:
			q.drain(ctx)

			return
		case <-ctx.Done():
			return
		}
	}
}

// drain evaluates whatever is still queued when shutdown begins, so a
// rolling restart doesn't lose deliveries GitLab was already told
// succeeded.
func (q *Queue) drain(ctx context.Context) {
	for {
		select {
		case event := <-q.events:
			q.evaluate(ctx, event)
		case <-ctx.Done():
			return
		default:
			return
		}
	}
}

// evaluate hands one activity to the engine, retrying a failure a few times
// before giving up on it.
//
// Retrying in process covers what actually goes wrong here: a database
// briefly unavailable, a lock contended by the backfill running alongside.
// An activity that still fails is reported at error with the dedup key it
// can be replayed from, rather than dropped silently.
func (q *Queue) evaluate(ctx context.Context, event activity.Event) {
	backoff := q.opts.RetryBackoff

	var err error

	for attempt := 1; attempt <= q.opts.MaxAttempts; attempt++ {
		err = q.processor.Process(ctx, event)
		if err == nil {
			q.processed.Add(1)

			return
		}

		// A cancelled context means the process is shutting down, not that
		// the activity is bad: stop retrying and let it be reported once.
		if ctx.Err() != nil {
			break
		}

		if attempt < q.opts.MaxAttempts {
			q.logger.Debug("retrying activity evaluation",
				zap.String("dedup_key", event.DedupKey),
				zap.Int("attempt", attempt),
				zap.Error(err),
			)

			if !sleep(ctx, backoff) {
				break
			}

			backoff *= 2
		}
	}

	q.failed.Add(1)
	q.logger.Error("dropping activity after repeated evaluation failures, it can be replayed by its dedup key",
		zap.String("dedup_key", event.DedupKey),
		zap.String("kind", string(event.Kind)),
		zap.Int64("actor_id", event.ActorID),
		zap.Int64("project_id", event.ProjectID),
		zap.Error(err),
	)
}

// sleep waits for d, reporting false if ctx was cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// loggerOrNop returns logger, or one that discards everything when none was
// configured, so callers never have to nil-check.
func loggerOrNop(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}

	return logger
}
