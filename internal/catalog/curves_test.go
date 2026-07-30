package catalog_test

import (
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
)

// The expected values are the first eleven tiers each curve produces in the
// VS Code Achievements extension this catalog's pacing is ported from. They
// are written out rather than recomputed so a change to the Go
// implementation shows up as a diff against the original curve, not as two
// implementations agreeing with each other.
func TestStandardCurves(t *testing.T) {
	tests := []struct {
		name  string
		curve catalog.Curve
		want  []int64
	}{
		{
			name:  "easy",
			curve: catalog.StandardEasyCurve,
			want:  []int64{1, 10, 100, 1_000, 10_000, 100_000, 1_000_000, 10_000_000, 100_000_000, 1_000_000_000, 10_000_000_000},
		},
		{
			name:  "medium",
			curve: catalog.StandardMediumCurve,
			want:  []int64{2, 10, 20, 100, 200, 1_000, 2_000, 10_000, 20_000, 100_000, 200_000},
		},
		{
			name:  "hard",
			curve: catalog.StandardHardCurve,
			want:  []int64{1, 5, 10, 50, 100, 500, 1_000, 5_000, 10_000, 50_000, 100_000},
		},
		{
			name:  "infernal",
			curve: catalog.StandardInfernalCurve,
			want:  []int64{1, 3, 5, 10, 50, 100, 500, 1_000, 5_000, 10_000, 50_000},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for tier, want := range tc.want {
				if got := tc.curve(int64(tier)); got != want {
					t.Errorf("tier %d: expected %d, got %d", tier, want, got)
				}
			}
		})
	}
}

func TestStandardCurves_AreMonotonic(t *testing.T) {
	curves := map[string]catalog.Curve{
		"easy":     catalog.StandardEasyCurve,
		"medium":   catalog.StandardMediumCurve,
		"hard":     catalog.StandardHardCurve,
		"infernal": catalog.StandardInfernalCurve,
	}

	for name, curve := range curves {
		t.Run(name, func(t *testing.T) {
			// A curve that ever flattened or dipped would put two tiers on
			// the same threshold, awarding both from one event forever.
			for tier := int64(1); tier <= 10; tier++ {
				previous, current := curve(tier-1), curve(tier)
				if current <= previous {
					t.Errorf("tier %d (%d) does not exceed tier %d (%d)", tier, current, tier-1, previous)
				}
			}
		})
	}
}

func TestSteps(t *testing.T) {
	curve := catalog.Steps(1, 7, 30, 365)

	for tier, want := range []int64{1, 7, 30, 365} {
		if got := curve(int64(tier)); got != want {
			t.Errorf("tier %d: expected %d, got %d", tier, want, got)
		}
	}

	if got := curve(9); got != 365 {
		t.Errorf("expected tiers past the end of the list to repeat the last value, got %d", got)
	}

	if got := curve(-1); got != 1 {
		t.Errorf("expected a negative tier to clamp to the first value, got %d", got)
	}
}
