package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/sections"
)

// TestLatestFastPathEligible pins the eligibility rules for the native
// /Items/Latest fast path. The synthetic recently-added section can only express
// a single library type + access scope, so any request carrying a filter it
// cannot reproduce must fall back to BrowseItems. Genre/name-prefix/person are
// the regression this guards: the section config cannot scope to them, so
// serving unfiltered recently-added would return a wrong, broader result set.
func TestLatestFastPathEligible(t *testing.T) {
	played := true
	base := func() itemsQuery { return itemsQuery{parentLibraryID: 7, startIndex: 0} }

	eligible := base()
	if !latestFastPathEligible(eligible, "movie", true) {
		t.Fatalf("a plain first-page movies-library Latest should be eligible")
	}
	if !latestFastPathEligible(base(), "series", true) {
		t.Fatalf("a plain first-page series-library Latest should be eligible")
	}

	cases := []struct {
		name    string
		libType string
		fetcher bool
		mutate  func(*itemsQuery)
	}{
		{name: "no sections fetcher", libType: "movie", fetcher: false},
		{name: "non-video library", libType: "", fetcher: true},
		{name: "ebook library", libType: "ebook", fetcher: true},
		{name: "deeper page", libType: "movie", fetcher: true, mutate: func(q *itemsQuery) { q.startIndex = 24 }},
		{name: "played filter", libType: "movie", fetcher: true, mutate: func(q *itemsQuery) { q.isPlayed = &played }},
		{name: "backdrop required", libType: "movie", fetcher: true, mutate: func(q *itemsQuery) { q.requireBackdrop = true }},
		{name: "genre filter", libType: "movie", fetcher: true, mutate: func(q *itemsQuery) { q.genreName = "Action" }},
		{name: "name prefix filter", libType: "movie", fetcher: true, mutate: func(q *itemsQuery) { q.namePrefix = "The" }},
		{name: "person filter", libType: "series", fetcher: true, mutate: func(q *itemsQuery) { q.personID = 42 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := base()
			if tc.mutate != nil {
				tc.mutate(&q)
			}
			if latestFastPathEligible(q, tc.libType, tc.fetcher) {
				t.Fatalf("%s must NOT be eligible for the native fast path", tc.name)
			}
		})
	}
}

// TestLatestRecentlyAddedConfigParity is the data-parity assertion for the
// native /Items/Latest fast path. A DB-backed comparison of the emitted rows is
// out of scope for a unit test, so instead we prove the two surfaces feed the
// SAME recently-added query the SAME type filter: the config the compat path
// builds must round-trip through the native ParseConfigFilters/filter_type that
// buildRecentlyAddedQuery reads. Same type filter + same library + same scope +
// same limit + same ORDER BY mil.first_seen_at DESC ⇒ identical membership and
// ordering.
func TestLatestRecentlyAddedConfigParity(t *testing.T) {
	cases := []struct {
		name           string
		itemTypes      []string
		wantFilterType string
	}{
		{name: "movies library", itemTypes: []string{"movie"}, wantFilterType: "movie"},
		{name: "series library", itemTypes: []string{"series"}, wantFilterType: "series"},
		{name: "empty/mixed library", itemTypes: nil, wantFilterType: ""},
		{name: "mixed explicit types", itemTypes: []string{"movie", "series"}, wantFilterType: ""},
		{name: "unsupported type", itemTypes: []string{"boxset"}, wantFilterType: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := latestRecentlyAddedConfig(tc.itemTypes)
			// Native reads the filter type via the shared ParseConfigFilters; the
			// compat config must decode to exactly the same value.
			got := sections.ParseConfigFilters(cfg).FilterType
			if got != tc.wantFilterType {
				t.Fatalf("latestRecentlyAddedConfig(%v) filter_type = %q, want %q", tc.itemTypes, got, tc.wantFilterType)
			}
		})
	}
}

// TestLatestRecentlyAddedConfigUsesScopedTypes documents that the config helper
// normalizes through the same compatScopedTypes clamp BrowseItems uses, so a
// concrete single video type is preserved while anything that clamps to a
// multi-type or no-match set falls back to the all-types (nil) config.
func TestLatestRecentlyAddedConfigUsesScopedTypes(t *testing.T) {
	if cfg := latestRecentlyAddedConfig([]string{"Movie"}); cfg == nil {
		t.Fatalf("a movie-typed library should yield a non-nil filter_type config")
	}
	if cfg := latestRecentlyAddedConfig(nil); cfg != nil {
		t.Fatalf("an untyped (mixed) library should yield a nil (all-types) config, got %s", cfg)
	}
}
