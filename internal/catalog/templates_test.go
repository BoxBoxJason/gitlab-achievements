package catalog_test

import (
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
)

func template() catalog.Template {
	return catalog.Template{
		CriteriaKey: "test_criteria",
		Title:       "Tester %s",
		Description: "Do the thing %d times.",
		Curve:       catalog.StandardHardCurve,
		MaxTier:     3,
	}
}

func TestExpand_DefaultsToTheStandardExpCurve(t *testing.T) {
	// The VS Code extension defaults expFunction to its infernal criteria
	// function; leaving ExpCurve unset has to mean the same thing here, or
	// every template would have to repeat it.
	entries := template().Expand()

	for i, entry := range entries {
		want := catalog.StandardInfernalCurve(int64(i))
		if entry.Exp != want {
			t.Errorf("expected tier %d to be worth %d EXP by default, got %d", entry.Tier, want, entry.Exp)
		}
	}
}

func TestExpand_HonoursAnExplicitExpCurve(t *testing.T) {
	tmpl := template()
	tmpl.ExpCurve = catalog.Steps(7, 11, 13, 17)

	entries := tmpl.Expand()

	for i, want := range []int64{7, 11, 13, 17} {
		if entries[i].Exp != want {
			t.Errorf("expected tier %d to be worth %d EXP, got %d", entries[i].Tier, want, entries[i].Exp)
		}
	}
}

func TestExpand_ReadsBothCurvesAtTheTierIndex(t *testing.T) {
	// Raising MinTier drops the easy tiers. What survives has to keep the
	// threshold and reward it already had, not slide down into the dropped
	// tiers' values just because it is now displayed as tier I.
	full := template().Expand()

	trimmed := template()
	trimmed.MinTier = 2
	entries := trimmed.Expand()

	if entries[0].Tier != 1 {
		t.Fatalf("expected the surviving tiers to be renumbered from I, got tier %d", entries[0].Tier)
	}

	if entries[0].Threshold != full[2].Threshold {
		t.Errorf("expected the surviving tier to keep threshold %d, got %d", full[2].Threshold, entries[0].Threshold)
	}

	if entries[0].Exp != full[2].Exp {
		t.Errorf("expected the surviving tier to keep its %d EXP, got %d", full[2].Exp, entries[0].Exp)
	}
}
