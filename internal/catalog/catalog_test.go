package catalog_test

import (
	"fmt"
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
