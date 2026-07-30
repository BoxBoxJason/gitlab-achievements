package webhook

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/boxboxjason/gitlab-achievements/internal/activity"
)

// countingProcessor records every activity it is handed and can be made to
// fail a fixed number of times first.
type countingProcessor struct {
	mu         sync.Mutex
	seen       []activity.Event
	attempts   int
	failFirst  int
	failAlways error
	block      chan struct{}
}

func (c *countingProcessor) Process(_ context.Context, event activity.Event) error {
	if c.block != nil {
		<-c.block
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.attempts++

	if c.failAlways != nil {
		return c.failAlways
	}

	if c.attempts <= c.failFirst {
		return errors.New("transient failure")
	}

	c.seen = append(c.seen, event)

	return nil
}

func (c *countingProcessor) processed() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.seen)
}

func (c *countingProcessor) tries() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.attempts
}

func testEvent(key string) activity.Event {
	return activity.Event{
		Kind: activity.KindPush, DedupKey: key, ActorID: 10, ProjectID: 1,
		OccurredAt: time.Now(),
	}
}

// drain shuts a queue down and waits for it, failing the test if it can't.
func drain(t *testing.T, queue *Queue) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := queue.Shutdown(ctx)
	if err != nil {
		t.Fatalf("expected the queue to drain, got: %v", err)
	}
}

func TestQueue_EvaluatesWhatItAccepts(t *testing.T) {
	processor := &countingProcessor{}
	queue := NewQueue(processor, Options{Workers: 1})
	queue.Start(t.Context())

	err := queue.Enqueue(t.Context(), []activity.Event{testEvent("a"), testEvent("b")})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	drain(t, queue)

	if processor.processed() != 2 {
		t.Errorf("expected both activities to be evaluated, got %d", processor.processed())
	}
}

func TestQueue_AcceptingIsIndependentOfEvaluating(t *testing.T) {
	// The whole point of the queue: a slow engine must not make GitLab wait.
	processor := &countingProcessor{block: make(chan struct{})}
	queue := NewQueue(processor, Options{Workers: 1})
	queue.Start(t.Context())

	accepted := make(chan error, 1)

	go func() {
		accepted <- queue.Enqueue(t.Context(), []activity.Event{testEvent("a")})
	}()

	select {
	case err := <-accepted:
		if err != nil {
			t.Errorf("expected the delivery to be accepted, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected accepting a delivery not to wait on the engine")
	}

	close(processor.block)
	drain(t, queue)
}

func TestQueue_RetriesATransientFailure(t *testing.T) {
	processor := &countingProcessor{failFirst: 2}
	queue := NewQueue(processor, Options{Workers: 1, MaxAttempts: 4, RetryBackoff: time.Millisecond})
	queue.Start(t.Context())

	err := queue.Enqueue(t.Context(), []activity.Event{testEvent("a")})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	drain(t, queue)

	if processor.processed() != 1 {
		t.Errorf("expected the activity to succeed on retry, got %d processed", processor.processed())
	}

	if processor.tries() != 3 {
		t.Errorf("expected 3 attempts (2 failures then success), got %d", processor.tries())
	}
}

func TestQueue_GivesUpAfterMaxAttempts(t *testing.T) {
	processor := &countingProcessor{failAlways: errors.New("database gone")}
	queue := NewQueue(processor, Options{Workers: 1, MaxAttempts: 3, RetryBackoff: time.Millisecond})
	queue.Start(t.Context())

	err := queue.Enqueue(t.Context(), []activity.Event{testEvent("a")})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	drain(t, queue)

	if processor.tries() != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", processor.tries())
	}

	if stats := queue.Stats(); stats.Failed != 1 {
		t.Errorf("expected the give-up to be counted so it isn't silent, got %+v", stats)
	}
}

func TestQueue_RefusesWhenFullRatherThanDropping(t *testing.T) {
	// A blocked worker plus a size-1 queue means the second activity has
	// nowhere to go.
	processor := &countingProcessor{block: make(chan struct{})}
	queue := NewQueue(processor, Options{Workers: 1, Size: 1})
	queue.Start(t.Context())

	// Fill the worker and the buffer.
	err := queue.Enqueue(t.Context(), []activity.Event{testEvent("a"), testEvent("b")})
	if err != nil {
		t.Fatalf("expected the first activities to be accepted, got: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err = queue.Enqueue(ctx, []activity.Event{testEvent("c")})
	if err == nil {
		t.Error("expected a full queue to refuse rather than silently drop the activity")
	}

	if stats := queue.Stats(); stats.Rejected != 1 {
		t.Errorf("expected the refusal to be counted, got %+v", stats)
	}

	close(processor.block)
	drain(t, queue)
}

func TestQueue_RefusesOnceShutdownHasBegun(t *testing.T) {
	processor := &countingProcessor{}
	queue := NewQueue(processor, Options{Workers: 1})
	queue.Start(t.Context())

	drain(t, queue)

	err := queue.Enqueue(t.Context(), []activity.Event{testEvent("a")})
	if !errors.Is(err, ErrShuttingDown) {
		t.Errorf("expected a delivery arriving during shutdown to be refused, got: %v", err)
	}
}

func TestQueue_ShutdownDoesNotPanicOnAnInFlightDelivery(t *testing.T) {
	// Shutdown must not close the events channel out from under a request
	// still being served, which would panic the whole server.
	processor := &countingProcessor{}
	queue := NewQueue(processor, Options{Workers: 2})
	queue.Start(t.Context())

	var wg sync.WaitGroup

	for range 20 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_ = queue.Enqueue(context.Background(), []activity.Event{testEvent("a")})
		}()
	}

	drain(t, queue)
	wg.Wait()
}

func TestQueue_DrainsWhatItAlreadyAcceptedOnShutdown(t *testing.T) {
	// GitLab has already been told these deliveries succeeded and will not
	// send them again, so a rolling restart must not discard them.
	processor := &countingProcessor{}
	queue := NewQueue(processor, Options{Workers: 1, Size: 64})

	events := make([]activity.Event, 0, 32)
	for i := range 32 {
		events = append(events, testEvent(string(rune('a'+i))))
	}

	err := queue.Enqueue(t.Context(), events)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	queue.Start(t.Context())
	drain(t, queue)

	if processor.processed() != 32 {
		t.Errorf("expected everything already accepted to be evaluated, got %d of 32", processor.processed())
	}
}

func TestQueue_DrainsWhatItAcceptedEvenWhenTheProcessIsAlreadySignalled(t *testing.T) {
	// The shutdown sequence a serving process actually runs: the workers'
	// context is the one SIGTERM cancels, and the drain only starts after
	// that, on a context of its own. Starting the workers on a context that
	// dies first would have them return before the drain rather than
	// because of it, leaving the queue full while Shutdown reported success
	// on a WaitGroup that nobody was left in.
	processor := &countingProcessor{}
	queue := NewQueue(processor, Options{Workers: 4, Size: 1024})

	// Whatever the workers run on, it must survive the signal that begins
	// shutdown; the queue is drained by Shutdown, not by cancellation.
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()

	queue.Start(workerCtx)

	events := make([]activity.Event, 0, 500)
	for i := range 500 {
		events = append(events, testEvent(strconv.Itoa(i)))
	}

	err := queue.Enqueue(context.Background(), events)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// SIGTERM: the context the rest of the process runs on is cancelled.
	signalCtx, signal := context.WithCancel(context.Background())
	signal()
	<-signalCtx.Done()

	drain(t, queue)

	stats := queue.Stats()
	if stats.Pending != 0 {
		t.Errorf("expected nothing left queued after a successful drain, got %d", stats.Pending)
	}

	if processor.processed() != 500 {
		t.Errorf("expected everything already accepted to be evaluated, got %d of 500", processor.processed())
	}
}

func TestQueue_EmptyEnqueueIsANoOp(t *testing.T) {
	queue := NewQueue(&countingProcessor{}, Options{Workers: 1})
	queue.Start(t.Context())

	err := queue.Enqueue(t.Context(), nil)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	drain(t, queue)
}

func TestQueue_ShutdownIsSafeToCallTwice(t *testing.T) {
	queue := NewQueue(&countingProcessor{}, Options{Workers: 1})
	queue.Start(t.Context())

	drain(t, queue)
	drain(t, queue)
}

func TestQueue_StopsRetryingWhenTheProcessIsGoingAway(t *testing.T) {
	// A queue that cannot make progress must still be bounded by the
	// deadline its caller set, rather than sitting out a retry schedule
	// measured in minutes. Shutdown reports the deadline it missed, and
	// abandons the evaluation instead of leaving it running.
	processor := &countingProcessor{failAlways: errors.New("database gone")}
	queue := NewQueue(processor, Options{Workers: 1, MaxAttempts: 100, RetryBackoff: time.Minute})

	queue.Start(t.Context())

	err := queue.Enqueue(context.Background(), []activity.Event{testEvent("a")})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Give the worker a moment to pick the activity up and start failing.
	time.Sleep(50 * time.Millisecond)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer shutdownCancel()

	started := time.Now()

	err = queue.Shutdown(shutdownCtx)
	if err == nil {
		t.Fatal("expected shutdown to report the activity it could not evaluate in time")
	}

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("expected shutdown to be bounded by its own deadline, took %v", elapsed)
	}
}

func TestQueue_KeepsDrainingAfterTheContextItWasStartedOnIsCancelled(t *testing.T) {
	// The workers' lifetime belongs to Shutdown, not to whatever context
	// happened to be in scope at Start. A caller has only one context to
	// hand, the one cancelled at SIGTERM, and honoring it here would empty
	// the workers out from under a queue that is still full.
	processor := &countingProcessor{}
	queue := NewQueue(processor, Options{Workers: 2, Size: 256})

	ctx, cancel := context.WithCancel(context.Background())
	queue.Start(ctx)

	// SIGTERM lands before anything has been enqueued, let alone evaluated.
	cancel()

	events := make([]activity.Event, 0, 128)
	for i := range 128 {
		events = append(events, testEvent(strconv.Itoa(i)))
	}

	err := queue.Enqueue(context.Background(), events)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	drain(t, queue)

	if processor.processed() != 128 {
		t.Errorf("expected the drain to outlive the cancelled start context, got %d of 128", processor.processed())
	}
}
