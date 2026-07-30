package catalog_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
)

func TestV1_NoDuplicateCriteriaTier(t *testing.T) {
	seen := make(map[string]bool)

	for _, e := range catalog.V1() {
		key := fmt.Sprintf("%s/%d", e.CriteriaKey, e.Tier)
		if seen[key] {
			t.Errorf("duplicate catalog entry for criteria %q tier %d", e.CriteriaKey, e.Tier)
		}

		seen[key] = true
	}
}

func TestV1_EntriesAreWellFormed(t *testing.T) {
	for _, e := range catalog.V1() {
		if e.CriteriaKey == "" {
			t.Errorf("entry %+v has an empty CriteriaKey", e)
		}

		if e.Name == "" {
			t.Errorf("entry %+v has an empty Name", e)
		}

		if e.Tier < 1 {
			t.Errorf("entry %+v has a non-positive Tier", e)
		}

		if e.Threshold < 1 {
			t.Errorf("entry %+v has a non-positive Threshold", e)
		}
	}
}

func TestV1_CoversEveryCriteria(t *testing.T) {
	criteria := []string{
		catalog.CriteriaCommits,
		catalog.CriteriaPushes,
		catalog.CriteriaBranchesCreated,
		catalog.CriteriaTagsCreated,
		catalog.CriteriaMergeRequestsOpened,
		catalog.CriteriaMergeRequestsMerged,
		catalog.CriteriaMergeRequestsApproved,
		catalog.CriteriaMergeRequestsClosed,
		catalog.CriteriaComments,
		catalog.CriteriaIssuesOpened,
		catalog.CriteriaIssuesClosed,
		catalog.CriteriaPipelinesRun,
		catalog.CriteriaPipelinesSucceeded,
		catalog.CriteriaPipelinesFailed,
		catalog.CriteriaActiveDays,
		catalog.CriteriaActivityStreak,
		catalog.CriteriaNightOwlDays,
		catalog.CriteriaEarlyBirdDays,
	}

	tiers := make(map[string]int)
	for _, entry := range catalog.V1() {
		tiers[entry.CriteriaKey]++
	}

	for _, criteriaKey := range criteria {
		if tiers[criteriaKey] == 0 {
			t.Errorf("criteria %q has no achievements", criteriaKey)
		}
	}

	if len(tiers) != len(criteria) {
		t.Errorf("expected exactly %d criteria in the catalog, got %d", len(criteria), len(tiers))
	}
}

func TestV1_ThresholdsRiseWithTier(t *testing.T) {
	// Tier order has to track threshold order: the engine awards every tier
	// whose threshold a counter reaches, so a lower tier that asked for
	// more would be earned after the tier above it.
	highest := make(map[string]int64)
	thresholds := make(map[string]int64)

	for _, entry := range catalog.V1() {
		if entry.Tier > highest[entry.CriteriaKey] {
			highest[entry.CriteriaKey] = entry.Tier
			thresholds[entry.CriteriaKey] = entry.Threshold

			continue
		}

		if entry.Tier == highest[entry.CriteriaKey] {
			t.Errorf("criteria %q has two tier %d entries", entry.CriteriaKey, entry.Tier)
		}
	}

	for _, entry := range catalog.V1() {
		if entry.Tier < highest[entry.CriteriaKey] && entry.Threshold >= thresholds[entry.CriteriaKey] {
			t.Errorf("criteria %q tier %d asks for %d, at or above its top tier's %d",
				entry.CriteriaKey, entry.Tier, entry.Threshold, thresholds[entry.CriteriaKey])
		}
	}
}

func TestV1_NamesAreUniqueAndTiered(t *testing.T) {
	// GitLab has no tier field, so the numeral in the name is the only
	// thing distinguishing one tier of an achievement from another, and
	// bootstrap keys its drift detection on the name being stable.
	seen := make(map[string]bool)

	for _, entry := range catalog.V1() {
		if seen[entry.Name] {
			t.Errorf("duplicate achievement name %q", entry.Name)
		}

		seen[entry.Name] = true

		if entry.Description == "" {
			t.Errorf("achievement %q has no description", entry.Name)
		}
	}
}

func TestV1_AvatarsResolve(t *testing.T) {
	for _, entry := range catalog.V1() {
		if !entry.HasAvatar() {
			continue
		}

		file, err := entry.Avatar()
		if err != nil {
			t.Errorf("achievement %q: %v", entry.Name, err)

			continue
		}

		file.Close()
	}
}

func TestV1_TiersAreNumberedFromOne(t *testing.T) {
	firstTiers := make(map[string]bool)

	for _, entry := range catalog.V1() {
		if entry.Tier == 1 {
			firstTiers[entry.CriteriaKey] = true

			if !strings.HasSuffix(entry.Name, " I") {
				t.Errorf("expected the first tier of %q to be numbered I, got %q", entry.CriteriaKey, entry.Name)
			}
		}
	}

	for _, entry := range catalog.V1() {
		if !firstTiers[entry.CriteriaKey] {
			t.Errorf("criteria %q has no tier I", entry.CriteriaKey)
		}
	}
}
