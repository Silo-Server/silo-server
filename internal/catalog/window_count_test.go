package catalog

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// placeholderRE matches Postgres positional parameter placeholders ($1, $2, ...)
// for use in arg-count assertions across countSQL tests.
var placeholderRE = regexp.MustCompile(`\$(\d+)`)

func TestBrowseRepository_browse_MaxLimitAllowsCallerSpecificLargePages(t *testing.T) {
	repo := &BrowseRepository{}
	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{Limit: 1000, MaxLimit: 1000})
	if err != nil {
		t.Fatalf("buildBrowsePlan error: %v", err)
	}
	if earlyEmpty {
		t.Fatalf("did not expect early empty result")
	}
	if plan.limit != 1000 {
		t.Fatalf("plan limit = %d, want 1000", plan.limit)
	}

	defaultPlan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{Limit: 1000})
	if err != nil {
		t.Fatalf("buildBrowsePlan default cap error: %v", err)
	}
	if earlyEmpty {
		t.Fatalf("did not expect early empty result for default cap")
	}
	if defaultPlan.limit != 100 {
		t.Fatalf("default plan limit = %d, want 100", defaultPlan.limit)
	}
}

// TestQueryExecutor_PreviewPage_CountSQL_OmitsSortOnlyJoins pins that exact
// totals do not carry ORDER BY-only joins. The page query still needs those
// joins for sorts such as added_at, but the count query only needs the base
// relation plus filter joins.
func TestQueryExecutor_PreviewPage_CountSQL_OmitsSortOnlyJoins(t *testing.T) {
	exec := &QueryExecutor{Scope: "movie", BaseRelationSQL: "media_items mi"}
	plan, err := exec.buildPreviewPagePlan(
		QueryDefinition{
			LibraryIDs: []int{1, 2, 3}, // exercises addedAtSortPlan's IN ($N, $M, $K)
			Sort:       QuerySort{Field: "added_at", Order: "desc"},
		},
		AccessFilter{},
		20, 0,
	)
	if err != nil {
		t.Fatalf("buildPreviewPagePlan error: %v", err)
	}
	if len(plan.sortArgs) == 0 {
		t.Fatalf("test setup: expected sort plan to require args (added_at + LibraryIDs); got sortArgs=%v", plan.sortArgs)
	}

	sql, args := plan.countSQL()

	if strings.Contains(sql, "sort_added") {
		t.Fatalf("countSQL must omit addedAtSortPlan's LEFT JOIN; got:\n%s", sql)
	}
	if len(args) != 1 {
		t.Fatalf("countSQL must omit sortArgs and bind only the library-scope arg; got %v", args)
	}

	// Verify args still cover every $N placeholder after dropping sort-only
	// joins and sortArgs.
	maxIdx := 0
	for _, m := range placeholderRE.FindAllStringSubmatch(sql, -1) {
		idx, _ := strconv.Atoi(m[1])
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	if maxIdx == 0 {
		t.Fatalf("expected at least one $N placeholder in countSQL; got:\n%s", sql)
	}
	if len(args) < maxIdx {
		t.Fatalf("countSQL references $%d but only %d args bound; sql:\n%s\nargs: %v",
			maxIdx, len(args), sql, args)
	}
}

// TestBrowseRepository_browse_CountSQL_DoesNotOverBindSortArgs pins that
// the count fallback's bound-args slice doesn't include orderArgs — the
// count SQL omits ORDER BY, so binding orderArgs would supply more
// parameters than the prepared statement references and pgx/Postgres
// would error with "bind message supplies N parameters, but prepared
// statement requires M".
//
// The canonical trigger is sort=random with a non-nil SnapshotAt:
// buildOrderByPlan emits ORDER BY md5(content_id || $N::text) and returns
// [snapshot] as orderArgs. Without orderArgs separated from args, the
// count fallback would over-bind by one.
//
// Regression guard for the post-perf-overhaul code review (Cursor Medium).
func TestBrowseRepository_browse_CountSQL_DoesNotOverBindSortArgs(t *testing.T) {
	repo := &BrowseRepository{}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{
		Limit:      20,
		Sort:       "random",
		SnapshotAt: &now,
	})
	if err != nil {
		t.Fatalf("buildBrowsePlan error: %v", err)
	}
	if earlyEmpty {
		t.Fatalf("did not expect early empty result")
	}
	if len(plan.orderArgs) == 0 {
		t.Fatalf("test setup: expected sort=random+SnapshotAt to produce an orderArg; got orderArgs=%v", plan.orderArgs)
	}

	sql, args := plan.countSQL()
	if strings.Contains(sql, "ORDER BY") {
		t.Fatalf("countSQL must omit ORDER BY (orderArgs would be referenced); got:\n%s", sql)
	}

	// Args must not exceed the highest $N placeholder in the SQL — pgx
	// rejects over-bind with a parameter-count mismatch.
	maxIdx := 0
	for _, m := range placeholderRE.FindAllStringSubmatch(sql, -1) {
		idx, _ := strconv.Atoi(m[1])
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	if len(args) > maxIdx {
		t.Fatalf("countSQL over-binds: %d args bound but max placeholder is $%d. sql:\n%s\nargs: %v",
			len(args), maxIdx, sql, args)
	}
}
