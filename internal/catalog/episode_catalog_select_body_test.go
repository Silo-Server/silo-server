package catalog

import (
	"strings"
	"testing"
)

// TestEpisodeCatalogSelectBodyExcludesNonSeriesEpisodes guards the invariant
// that the episode catalog only ever hydrates episodes whose parent is a TV
// series. episode_libraries (and episode_catalog_entries) also contain podcast
// episodes (media_items.type = 'podcast'); the `si.type = 'series'` predicate in
// episodeCatalogSelectBody is what keeps those non-TV rows out of episode-scoped
// results regardless of which library a query resolves. Dropping it would let
// podcast/audiobook episodes surface in a TV "episode" rail (fail-open).
func TestEpisodeCatalogSelectBodyExcludesNonSeriesEpisodes(t *testing.T) {
	if !strings.Contains(episodeCatalogBaseRelation, "si.type = 'series'") {
		t.Fatalf("episodeCatalogBaseRelation must guard si.type = 'series' so non-TV "+
			"(podcast/audiobook) episodes never hydrate; got %q", episodeCatalogBaseRelation)
	}

	// The library-scoped relation and the hydration relation render from the same
	// template, so the guard must travel with every rendering.
	scoped, _, handled := episodeCatalogBaseRelationForLibraries([]int{2}, nil, 1)
	if !handled {
		t.Fatal("expected episode library scope to be handled")
	}
	if !strings.Contains(scoped, "si.type = 'series'") {
		t.Fatalf("library-scoped episode relation must keep the si.type = 'series' "+
			"guard; got %q", scoped)
	}
}
