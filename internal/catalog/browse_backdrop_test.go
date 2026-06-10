package catalog

import (
	"strings"
	"testing"
)

// TestBuildBrowsePlan_RequireBackdrop asserts the ImageTypes=Backdrop filter
// (BrowseFilters.RequireBackdrop) renders the backdrop-presence predicate into
// the WHERE clause, and is absent otherwise. Guards against a future refactor
// of buildBrowsePlan silently dropping the condition.
func TestBuildBrowsePlan_RequireBackdrop(t *testing.T) {
	const predicate = "NULLIF(BTRIM(mi.backdrop_path), '') IS NOT NULL"
	repo := &BrowseRepository{}

	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{Type: "movie", RequireBackdrop: true})
	if err != nil || earlyEmpty {
		t.Fatalf("buildBrowsePlan(RequireBackdrop) err=%v earlyEmpty=%v", err, earlyEmpty)
	}
	if !strings.Contains(plan.whereClause, predicate) {
		t.Fatalf("RequireBackdrop=true: whereClause missing predicate.\ngot: %s", plan.whereClause)
	}

	plan, _, err = repo.buildBrowsePlan(BrowseFilters{Type: "movie"})
	if err != nil {
		t.Fatalf("buildBrowsePlan err=%v", err)
	}
	if strings.Contains(plan.whereClause, predicate) {
		t.Fatalf("RequireBackdrop unset: predicate should be absent.\ngot: %s", plan.whereClause)
	}
}
