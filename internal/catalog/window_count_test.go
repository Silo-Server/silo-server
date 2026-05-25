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

// TestQueryExecutor_PreviewPage_UsesWindowCount asserts that buildPreviewPageSQL
// emits a single-pass paged SELECT that includes COUNT(*) OVER () so PreviewPage
// no longer needs a separate count query when includeTotal is true.
func TestQueryExecutor_PreviewPage_UsesWindowCount(t *testing.T) {
	exec := &QueryExecutor{Scope: "movie", BaseRelationSQL: "media_items mi"}
	sql, _, err := exec.buildPreviewPageSQL(QueryDefinition{}, AccessFilter{}, 20, 0, true /* includeTotal */)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	if !strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("expected COUNT(*) OVER () for single-pass count; got:\n%s", sql)
	}
}

// TestQueryExecutor_PreviewPage_SkipTotal_OmitsWindowCount asserts that when
// includeTotal is false the window count is not emitted (we don't pay for the
// scan when the caller doesn't need a total).
func TestQueryExecutor_PreviewPage_SkipTotal_OmitsWindowCount(t *testing.T) {
	exec := &QueryExecutor{Scope: "movie", BaseRelationSQL: "media_items mi"}
	sql, _, err := exec.buildPreviewPageSQL(QueryDefinition{}, AccessFilter{}, 20, 0, false /* includeTotal */)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL error: %v", err)
	}
	if strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("SkipTotal must omit COUNT(*) OVER (); got:\n%s", sql)
	}
}

// TestBrowseRepository_browse_UsesWindowCount asserts that buildBrowsePlan +
// pagedSQL emit COUNT(*) OVER () in the data SELECT when includeTotal is true.
func TestBrowseRepository_browse_UsesWindowCount(t *testing.T) {
	repo := &BrowseRepository{}
	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{Limit: 20})
	if err != nil {
		t.Fatalf("buildBrowsePlan error: %v", err)
	}
	if earlyEmpty {
		t.Fatalf("did not expect early empty result for default filters")
	}
	sql, _ := plan.pagedSQL(true)
	if !strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("expected COUNT(*) OVER () for single-pass browse count; got:\n%s", sql)
	}
}

// TestBrowseRepository_browse_SkipTotal_OmitsWindowCount asserts that when
// includeTotal is false the browse pagedSQL does not include the window count.
func TestBrowseRepository_browse_SkipTotal_OmitsWindowCount(t *testing.T) {
	repo := &BrowseRepository{}
	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{Limit: 20})
	if err != nil {
		t.Fatalf("buildBrowsePlan error: %v", err)
	}
	if earlyEmpty {
		t.Fatalf("did not expect early empty result for default filters")
	}
	sql, _ := plan.pagedSQL(false)
	if strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("SkipTotal must omit COUNT(*) OVER (); got:\n%s", sql)
	}
}

// TestQueryExecutor_PreviewPage_CountSQL_BindsSortJoinArgs pins that
// previewPagePlan.countSQL binds the sort-plan join args (sortArgs) — not
// just cteArgs+args. fromClausePaged embeds sort-plan join clauses
// (added_at, progress, date_viewed, plays, resolution, bitrate all need
// LIBRARY-id-bound joins), so omitting sortArgs would leave bound
// placeholders inside the FROM clause unfilled and Postgres would error
// out with "missing argument" at the count fallback path.
//
// Regression guard for the post-perf-overhaul code review (macroscope High).
func TestQueryExecutor_PreviewPage_CountSQL_BindsSortJoinArgs(t *testing.T) {
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

	// The sort-plan LEFT JOIN must be embedded in the count SQL — that's the
	// reason sortArgs binding matters at all.
	if !strings.Contains(sql, "sort_added") {
		t.Fatalf("countSQL must include addedAtSortPlan's LEFT JOIN; got:\n%s", sql)
	}

	// Verify args length covers every $N placeholder in the SQL. If we
	// dropped sortArgs, the highest $N would exceed len(args) and Postgres
	// would fail with "missing argument".
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
		t.Fatalf("countSQL references $%d but only %d args bound (sortArgs likely dropped); sql:\n%s\nargs: %v",
			maxIdx, len(args), sql, args)
	}
}

// TestQueryExecutor_PreviewPage_CountSQL_OmitsLimitOffsetOrderBy pins the
// empty-page fallback SQL shape on previewPagePlan. When pagedSQL(true)
// returns an empty page past offset 0, the executor invokes countSQL() to
// recover the real total — COUNT(*) OVER () would otherwise emit no rows
// and leave the caller seeing total=0 even when broader matches exist.
//
// The countSQL must:
//   - omit LIMIT/OFFSET (we want the unpaginated total)
//   - omit ORDER BY (irrelevant for a count, and may reference unbound args)
//   - wrap the inner FROM/WHERE in `SELECT COUNT(*) FROM (SELECT 1 ...) sub`
//     so any GROUP BY in the inner query counts groups (not rows), matching
//     what COUNT(*) OVER () would have computed.
func TestQueryExecutor_PreviewPage_CountSQL_OmitsLimitOffsetOrderBy(t *testing.T) {
	exec := &QueryExecutor{Scope: "movie", BaseRelationSQL: "media_items mi"}
	plan, err := exec.buildPreviewPagePlan(QueryDefinition{}, AccessFilter{}, 20, 0)
	if err != nil {
		t.Fatalf("buildPreviewPagePlan error: %v", err)
	}
	sql, _ := plan.countSQL()
	if !strings.Contains(sql, "SELECT COUNT(*) FROM (SELECT 1") {
		t.Fatalf("expected SELECT COUNT(*) FROM (SELECT 1 ...) wrapper; got:\n%s", sql)
	}
	if strings.Contains(sql, "LIMIT") {
		t.Fatalf("countSQL must omit LIMIT; got:\n%s", sql)
	}
	if strings.Contains(sql, "OFFSET") {
		t.Fatalf("countSQL must omit OFFSET; got:\n%s", sql)
	}
	if strings.Contains(sql, "ORDER BY") {
		t.Fatalf("countSQL must omit ORDER BY; got:\n%s", sql)
	}
	if strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("countSQL must use plain COUNT(*) (not the window form); got:\n%s", sql)
	}
}

// TestBrowseRepository_browse_CountSQL_OmitsLimitOffsetOrderBy pins the same
// empty-page fallback contract on browseQueryPlan.
func TestBrowseRepository_browse_CountSQL_OmitsLimitOffsetOrderBy(t *testing.T) {
	repo := &BrowseRepository{}
	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{Limit: 20})
	if err != nil {
		t.Fatalf("buildBrowsePlan error: %v", err)
	}
	if earlyEmpty {
		t.Fatalf("did not expect early empty result")
	}
	sql, _ := plan.countSQL()
	if !strings.Contains(sql, "SELECT COUNT(*) FROM (SELECT 1") {
		t.Fatalf("expected SELECT COUNT(*) FROM (SELECT 1 ...) wrapper; got:\n%s", sql)
	}
	if strings.Contains(sql, "LIMIT") {
		t.Fatalf("countSQL must omit LIMIT; got:\n%s", sql)
	}
	if strings.Contains(sql, "OFFSET") {
		t.Fatalf("countSQL must omit OFFSET; got:\n%s", sql)
	}
	if strings.Contains(sql, "ORDER BY") {
		t.Fatalf("countSQL must omit ORDER BY; got:\n%s", sql)
	}
	if strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("countSQL must use plain COUNT(*) (not the window form); got:\n%s", sql)
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

// TestBuildBrowseFavoritesPlan_CountSQL_OmitsLimitOffsetOrderBy pins the
// empty-page fallback contract on browseFavoritesPlan.
func TestBuildBrowseFavoritesPlan_CountSQL_OmitsLimitOffsetOrderBy(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID: 1, ProfileID: "p1", Limit: 20,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan error: %v", err)
	}
	sql, _ := plan.countSQL()
	if !strings.Contains(sql, "SELECT COUNT(*) FROM (SELECT 1") {
		t.Fatalf("expected SELECT COUNT(*) FROM (SELECT 1 ...) wrapper; got:\n%s", sql)
	}
	if strings.Contains(sql, "LIMIT") {
		t.Fatalf("countSQL must omit LIMIT; got:\n%s", sql)
	}
	if strings.Contains(sql, "OFFSET") {
		t.Fatalf("countSQL must omit OFFSET; got:\n%s", sql)
	}
	if strings.Contains(sql, "ORDER BY") {
		t.Fatalf("countSQL must omit ORDER BY; got:\n%s", sql)
	}
	if strings.Contains(sql, "COUNT(*) OVER ()") {
		t.Fatalf("countSQL must use plain COUNT(*); got:\n%s", sql)
	}
}

// TestItemRepo_Search_CountSQL_OmitsLimitOffsetOrderBy pins the same
// empty-page fallback contract for the Search path. The count sibling must
// preserve the title-gate CROSS JOIN filter so the recovered total reflects
// the post-filter row count (matching COUNT(*) OVER () semantics on the
// data SELECT). Single-word and multi-word queries share one SQL shape.
func TestItemRepo_Search_CountSQL_OmitsLimitOffsetOrderBy(t *testing.T) {
	repo := &ItemRepository{}

	for _, query := range []string{"avatar", "the matrix reloaded"} {
		t.Run(query, func(t *testing.T) {
			_, countSQL, _ := repo.buildSearchSQL(query, []string{"movie"}, 20, 0, AccessFilter{})
			if !strings.Contains(countSQL, "WITH scored AS") {
				t.Fatalf("countSQL must include scored CTE; got:\n%s", countSQL)
			}
			if !strings.Contains(countSQL, "stats AS") {
				t.Fatalf("countSQL must include stats CTE; got:\n%s", countSQL)
			}
			if !strings.Contains(countSQL, "CROSS JOIN stats") {
				t.Fatalf("countSQL must CROSS JOIN stats so the recovered total reflects the post-filter set; got:\n%s", countSQL)
			}
			if !strings.Contains(countSQL, "has_title_match") {
				t.Fatalf("countSQL must apply the title-gate predicate; got:\n%s", countSQL)
			}
			if !strings.Contains(countSQL, "SELECT COUNT(*)") {
				t.Fatalf("expected SELECT COUNT(*); got:\n%s", countSQL)
			}
			if strings.Contains(countSQL, "LIMIT ") {
				t.Fatalf("countSQL must omit LIMIT; got:\n%s", countSQL)
			}
			if strings.Contains(countSQL, "OFFSET ") {
				t.Fatalf("countSQL must omit OFFSET; got:\n%s", countSQL)
			}
			if strings.Contains(countSQL, "ORDER BY") {
				t.Fatalf("countSQL must omit ORDER BY; got:\n%s", countSQL)
			}
			if strings.Contains(countSQL, "COUNT(*) OVER ()") {
				t.Fatalf("countSQL must use plain COUNT(*); got:\n%s", countSQL)
			}
		})
	}
}
