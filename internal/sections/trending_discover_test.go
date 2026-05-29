package sections

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestOrderedTrendingContentIDs_PreservesOrderAndSeriesPrefersTVDB(t *testing.T) {
	entries := []trendingDiscoverEntry{
		{tmdbID: "1", mediaType: "movie"},
		{tmdbID: "2", tvdbID: "20", mediaType: "tv"},
		{imdbID: "tt3", mediaType: "movie"},
	}
	movieLookup := &catalog.ExternalIDLookup{
		ByTMDB: map[string]string{"1": "cm1"},
		ByIMDb: map[string]string{"tt3": "cm3"},
		ByTVDB: map[string]string{},
	}
	seriesLookup := &catalog.ExternalIDLookup{
		ByTVDB: map[string]string{"20": "cs2"},
		ByTMDB: map[string]string{"2": "cs2_tmdb"}, // TVDB should win for series
		ByIMDb: map[string]string{},
	}
	got := orderedTrendingContentIDs(entries, movieLookup, seriesLookup)
	want := []string{"cm1", "cs2", "cm3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pos %d = %q want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestOrderedTrendingContentIDs_SkipsUnmatchedAndDedups(t *testing.T) {
	entries := []trendingDiscoverEntry{
		{tmdbID: "1", mediaType: "movie"},   // matches cX
		{tmdbID: "404", mediaType: "movie"}, // no match -> skipped
		{imdbID: "ttX", mediaType: "movie"}, // also resolves to cX -> deduped
	}
	movieLookup := &catalog.ExternalIDLookup{
		ByTMDB: map[string]string{"1": "cX"},
		ByIMDb: map[string]string{"ttX": "cX"},
		ByTVDB: map[string]string{},
	}
	got := orderedTrendingContentIDs(entries, movieLookup, &catalog.ExternalIDLookup{})
	if len(got) != 1 || got[0] != "cX" {
		t.Fatalf("expected [cX], got %v", got)
	}
}

func TestOrderedTrendingContentIDs_MovieIgnoresTVDB(t *testing.T) {
	entries := []trendingDiscoverEntry{
		{tvdbID: "50", tmdbID: "5", mediaType: "movie"},
	}
	movieLookup := &catalog.ExternalIDLookup{
		ByTVDB: map[string]string{"50": "cTV"}, // must be ignored for movies
		ByTMDB: map[string]string{"5": "cTMDB"},
		ByIMDb: map[string]string{},
	}
	got := orderedTrendingContentIDs(entries, movieLookup, &catalog.ExternalIDLookup{})
	if len(got) != 1 || got[0] != "cTMDB" {
		t.Fatalf("movie should match TMDB not TVDB; got %v", got)
	}
}
