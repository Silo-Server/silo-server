package catalog

import (
	"testing"
)

func TestDeferredEpisodeHydrationScope(t *testing.T) {
	for _, tc := range []struct {
		scope, sort string
		offset      int
		want        bool
	}{
		{"episode", "title", 20, true}, {"episode", "title", 0, false},
		{"movie", "title", 20, false}, {"manga", "title", 20, false},
		{"episode", "year", 20, false}, {"episode", "added_at", 20, false},
	} {
		plan, err := (&QueryExecutor{Scope: tc.scope}).buildPreviewPagePlan(QueryDefinition{MediaScope: tc.scope, Sort: QuerySort{Field: tc.sort}}, AccessFilter{}, 20, tc.offset)
		if err != nil {
			t.Fatal(err)
		}
		if plan.deferEpisodeHydration != tc.want {
			t.Fatalf("scope=%s sort=%s offset=%d: deferred=%v, want %v", tc.scope, tc.sort, tc.offset, plan.deferEpisodeHydration, tc.want)
		}
	}
}
