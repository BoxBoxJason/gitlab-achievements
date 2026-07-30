package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestEvery_DoesNotFireImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32

	done := make(chan struct{})

	go func() {
		Every(ctx, time.Hour, func(context.Context) error {
			calls.Add(1)

			return nil
		}, nil)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected Every to return promptly after context cancellation")
	}

	if calls.Load() != 0 {
		t.Errorf("expected fn not to be called before the first tick, got %d calls", calls.Load())
	}
}

func TestEvery_FiresOnEachTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32

	done := make(chan struct{})

	go func() {
		Every(ctx, 10*time.Millisecond, func(context.Context) error {
			calls.Add(1)

			return nil
		}, nil)
		close(done)
	}()

	time.Sleep(55 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected Every to return promptly after context cancellation")
	}

	if calls.Load() < 2 {
		t.Errorf("expected at least 2 ticks to have fired, got %d", calls.Load())
	}
}

func TestEvery_ReportsErrorsWithoutStopping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errCalls atomic.Int32

	boom := errors.New("boom")
	done := make(chan struct{})

	go func() {
		Every(ctx, 10*time.Millisecond, func(context.Context) error {
			return boom
		}, func(err error) {
			if !errors.Is(err, boom) {
				t.Errorf("expected the boom error, got %v", err)
			}

			errCalls.Add(1)
		})
		close(done)
	}()

	time.Sleep(35 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected Every to return promptly after context cancellation")
	}

	if errCalls.Load() < 2 {
		t.Errorf("expected onError to have been called more than once (loop kept running after an error), got %d", errCalls.Load())
	}
}

func TestEvery_StopsPromptlyOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})

	go func() {
		Every(ctx, time.Minute, func(context.Context) error { return nil }, nil)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected Every to return promptly when the context is already cancelled")
	}
}
