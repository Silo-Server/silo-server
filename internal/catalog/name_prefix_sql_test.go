package catalog

import (
	"strings"
	"testing"
)

// TestQueryExecutor_NamePrefix_PushedIntoWHERE asserts that when AccessFilter
// carries a NamePrefix, the resulting paged SQL contains a prefix-anchored
// dual-column LIKE predicate: first arm matches the idx_media_items_sort_key
// expression (migration 102), second arm matches LOWER(title) /
// idx_media_items_search_exact_title (migration 001). Both arms are required
// so items with a curated sort_title that differs from title (e.g.
// title="The Office", sort_title="Office, The") are not silently dropped on
// prefix="the". The LIKE pattern argument is anchored with no leading wildcard.
func TestQueryExecutor_NamePrefix_PushedIntoWHERE(t *testing.T) {
	exec := &QueryExecutor{Scope: "movie", BaseRelationSQL: "media_items mi"}
	access := AccessFilter{NamePrefix: "Star"}

	sql, args, err := exec.buildPreviewPageSQL(QueryDefinition{}, access, 20, 0, true)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL returned error: %v", err)
	}

	// First arm: sort-key expression matching idx_media_items_sort_key.
	if !strings.Contains(sql, "LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) LIKE") {
		t.Fatalf("expected sort-key LIKE arm matching idx_media_items_sort_key; got %q", sql)
	}
	// Second arm: LOWER(title) matching idx_media_items_search_exact_title;
	// required so curated sort_title doesn't drop title-prefix matches.
	if !strings.Contains(sql, "LOWER(mi.title) LIKE") {
		t.Fatalf("expected LOWER(mi.title) LIKE arm; got %q", sql)
	}
	if strings.Contains(sql, "LIKE '%") {
		t.Fatalf("expected LIKE pattern to be parameterized (no leading literal wildcard); got %q", sql)
	}

	foundArg := false
	for _, a := range args {
		s, ok := a.(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.ToLower(s), "star") &&
			strings.HasSuffix(s, "%") &&
			!strings.HasPrefix(s, "%") {
			foundArg = true
			break
		}
	}
	if !foundArg {
		t.Fatalf("expected an args entry like \"star%%\" (lowercased, trailing-only wildcard); got args=%v", args)
	}
}

func TestQueryExecutor_NamePrefix_UsesEpisodeSortKeyForEpisodeScope(t *testing.T) {
	exec := &QueryExecutor{}
	access := AccessFilter{NamePrefix: "Pilot"}

	sql, args, err := exec.buildPreviewPageSQL(
		QueryDefinition{MediaScope: "episode", LibraryIDs: []int{2}},
		access,
		20,
		0,
		true,
	)
	if err != nil {
		t.Fatalf("buildPreviewPageSQL returned error: %v", err)
	}
	if !strings.Contains(sql, "mi.sort_key LIKE") {
		t.Fatalf("expected episode prefix to use projected sort_key; got %q", sql)
	}
	if strings.Contains(sql, "BTRIM(mi.sort_title)") {
		t.Fatalf("episode prefix should not recompute sort_title expression; got %q", sql)
	}
	if len(args) < 2 || args[1] != "pilot%" {
		t.Fatalf("expected episode library args followed by prefix arg; got %v", args)
	}
}

// TestBrowseFilters_NamePrefix_BothArmsSargable asserts that the dual-column
// LIKE in BrowseRepository's WHERE clause uses an expression that matches
// idx_media_items_sort_key (migration 102) on the first arm and
// idx_media_items_search_exact_title (migration 001, on LOWER(title)) on the
// second arm. Both arms must be index-using; otherwise the planner falls back
// to a seqscan on /Items?NameStartsWith=X queries against ~200K-row catalogs.
//
// Regression guard for the post-perf-overhaul code review (2026-05): the
// initial first-arm form was LOWER(COALESCE(NULLIF(BTRIM(sort_title),”), ”))
// which fell back to ” instead of title and matched no index expression.
func TestBrowseFilters_NamePrefix_BothArmsSargable(t *testing.T) {
	_, where, _, earlyEmpty := filterWhereClauseForSource(
		BrowseFilters{NamePrefix: "Star"}, "media_items mi", "")
	if earlyEmpty {
		t.Fatalf("unexpected earlyEmpty for NamePrefix-only filter")
	}
	// First arm matches idx_media_items_sort_key.
	if !strings.Contains(where, "LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) LIKE") {
		t.Fatalf("expected sort-key LIKE arm matching idx_media_items_sort_key; got %s", where)
	}
	// Second arm matches idx_media_items_search_exact_title.
	if !strings.Contains(where, "LOWER(mi.title) LIKE") {
		t.Fatalf("expected LOWER(mi.title) LIKE arm; got %s", where)
	}
	// Reject the previous broken form that used '' as the COALESCE fallback.
	if strings.Contains(where, "BTRIM(mi.sort_title), ''), ''))") {
		t.Fatalf("first arm still uses '' fallback (defeats idx_media_items_sort_key); got %s", where)
	}
}

// TestEscapePrefixForLike checks that the helper lowercases input and escapes
// the LIKE wildcards (% _) and the escape character itself (\).
func TestEscapePrefixForLike(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Star", "star"},
		{"  Star  ", "star"},
		{"50%", `50\%`},
		{"a_b", `a\_b`},
		{`back\slash`, `back\\slash`},
		{`%_\`, `\%\_\\`},
	}
	for _, tc := range cases {
		got := escapePrefixForLike(tc.in)
		if got != tc.want {
			t.Errorf("escapePrefixForLike(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
