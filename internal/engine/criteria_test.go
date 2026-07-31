package engine

import (
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
)

// TestKindCriteria_EveryCatalogCriteriaHasAProducer is the guard against the
// half-finished half of adding an achievement: a template lands in the
// catalog, bootstrap dutifully creates the GitLab achievements for its ten
// tiers, and nothing ever advances the counter, so every user on the
// instance sees ten achievements that cannot be earned.
//
// A criteria earns its place either by being reachable from some activity
// kind or by being derived from the user's activity days.
func TestKindCriteria_EveryCatalogCriteriaHasAProducer(t *testing.T) {
	produced := make(map[string]bool)

	for _, criteria := range kindCriteria {
		for _, criteriaKey := range criteria {
			produced[criteriaKey] = true
		}
	}

	for _, criteriaKey := range dayCriteria {
		produced[criteriaKey] = true
	}

	for _, entry := range catalog.V1() {
		if !produced[entry.CriteriaKey] {
			t.Errorf("catalog criteria %q is awarded by nothing: no activity kind advances it", entry.CriteriaKey)
		}
	}
}

// TestKindCriteria_EveryMappedCriteriaIsInTheCatalog is the same guard from
// the other side: a criteria nothing in the catalog is keyed on means the
// engine counts activity towards an achievement that does not exist.
func TestKindCriteria_EveryMappedCriteriaIsInTheCatalog(t *testing.T) {
	inCatalog := make(map[string]bool)
	for _, entry := range catalog.V1() {
		inCatalog[entry.CriteriaKey] = true
	}

	for kind, criteria := range kindCriteria {
		for _, criteriaKey := range criteria {
			if !inCatalog[criteriaKey] {
				t.Errorf("activity kind %q advances %q, which no achievement is earned off", kind, criteriaKey)
			}
		}
	}
}
