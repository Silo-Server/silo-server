package jellycompat

import "testing"

// TestFavoriteBrowseFiltersSupportedBySQL_RejectsUnsupportedSorts pins that
// the SQL fast path is bypassed for sort fields that catalog's
// buildBrowseFavoritesOrderBy doesn't implement (rating_imdb, random) —
// otherwise BrowseFavorites would silently fall through to added_at,
// changing observed ordering vs. the legacy two-query path that delegated
// to a broader sorter.
//
// Regression guard for the post-perf-overhaul code review (Codex P2).
func TestFavoriteBrowseFiltersSupportedBySQL_RejectsUnsupportedSorts(t *testing.T) {
	cases := []struct {
		sort string
		want bool
	}{
		// Supported by buildBrowseFavoritesOrderBy.
		{"", true},
		{"added_at", true},

		// mapSortBy emits these but BrowseFavorites doesn't implement them —
		// the SQL path would silently sort by added_at instead.
		{"rating_imdb", false},
		{"random", false},
	}
	for _, tc := range cases {
		got := favoriteBrowseFiltersSupportedBySQL(itemsQuery{sort: tc.sort})
		if got != tc.want {
			t.Errorf("favoriteBrowseFiltersSupportedBySQL(sort=%q) = %v, want %v", tc.sort, got, tc.want)
		}
	}
}

// TestFavoriteBrowseFiltersSupportedBySQL_NonSortGuardsStillFire asserts the
// existing isPlayed/personID/specificIDs guards still fire (regression guard
// against accidentally reordering or removing them when adding the sort
// allowlist).
func TestFavoriteBrowseFiltersSupportedBySQL_NonSortGuardsStillFire(t *testing.T) {
	played := true
	if favoriteBrowseFiltersSupportedBySQL(itemsQuery{isPlayed: &played, sort: "added_at"}) {
		t.Errorf("isPlayed must disable SQL path")
	}
	if favoriteBrowseFiltersSupportedBySQL(itemsQuery{personID: 42, sort: "added_at"}) {
		t.Errorf("personID must disable SQL path")
	}
	if favoriteBrowseFiltersSupportedBySQL(itemsQuery{specificIDs: []string{"abc"}, sort: "added_at"}) {
		t.Errorf("specificIDs must disable SQL path")
	}
}
