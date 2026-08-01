package activity

import (
	"context"
	"errors"
	"testing"
)

func TestEvent_Weight(t *testing.T) {
	tests := []struct {
		name  string
		count int64
		want  int64
	}{
		{"unset means one occurrence", 0, 1},
		{"negative is not a valid count", -3, 1},
		{"a batched push carries its commit count", 7, 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Event{Count: tc.count}).Weight(); got != tc.want {
				t.Errorf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestProcessorFunc_Process(t *testing.T) {
	var seen Event

	wantErr := errors.New("boom")

	var processor Processor = ProcessorFunc(func(_ context.Context, event Event) error {
		seen = event

		return wantErr
	})

	event := Event{Kind: KindCommit, DedupKey: "push:7:refs/heads/main:abc:commit"}

	err := processor.Process(t.Context(), event)
	if !errors.Is(err, wantErr) {
		t.Errorf("expected the function's error to be returned, got: %v", err)
	}

	if seen != event {
		t.Errorf("expected the event to be passed through, got %+v", seen)
	}
}
