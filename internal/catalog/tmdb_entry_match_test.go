package catalog

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
)

// fakeExternalIDResolver answers GetByExternalIDs from fixed maps per type and
// lists withoutTMDB as the catalog's titles lacking a TMDB ID.
type fakeExternalIDResolver struct {
	byType      map[string]*ExternalIDLookup
	withoutTMDB map[string][]TitleWithoutTMDBID
	calls       int
}

func (f *fakeExternalIDResolver) GetByExternalIDs(_ context.Context, batch ExternalIDBatch, itemType string) (*ExternalIDLookup, error) {
	f.calls++
	out := &ExternalIDLookup{ByTMDB: map[string]string{}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}}
	known := f.byType[itemType]
	if known == nil {
		return out, nil
	}
	for _, id := range batch.TMDBIDs {
		if v := known.ByTMDB[id]; v != "" {
			out.ByTMDB[id] = v
		}
	}
	for _, id := range batch.IMDbIDs {
		if v := known.ByIMDb[id]; v != "" {
			out.ByIMDb[id] = v
		}
	}
	for _, id := range batch.TVDBIDs {
		if v := known.ByTVDB[id]; v != "" {
			out.ByTVDB[id] = v
		}
	}
	return out, nil
}

func (f *fakeExternalIDResolver) TitlesWithoutTMDBID(_ context.Context, itemType string, limit int) ([]TitleWithoutTMDBID, error) {
	titles := f.withoutTMDB[itemType]
	return titles[:min(limit, len(titles))], nil
}

// enrichingFetcher records the entries it enriches, giving each an IMDb ID of
// "tt" plus its TMDB ID.
type enrichingFetcher struct {
	asked []int
	err   error
}

func (f *enrichingFetcher) EnrichExternalIDs(_ context.Context, entries []TMDBCollectionEntry) error {
	for i := range entries {
		f.asked = append(f.asked, entries[i].ID)
		entries[i].IMDbID = "tt" + strconv.Itoa(entries[i].ID)
	}
	return f.err
}

// findingFetcher also finds TMDB IDs for titles: IMDb "ttN" is TMDB N.
type findingFetcher struct {
	enrichingFetcher
	found []string
}

func (f *findingFetcher) FindTMDBIDs(_ context.Context, _ string, titles []TitleWithoutTMDBID) ([]int, error) {
	ids := make([]int, len(titles))
	for i, title := range titles {
		f.found = append(f.found, title.ContentID)
		ids[i], _ = strconv.Atoi(title.IMDbID[2:])
	}
	return ids, nil
}

func TestMatchTMDBEntriesMakesNoTMDBRequestsWhenEveryTitleHasATMDBID(t *testing.T) {
	items := &fakeExternalIDResolver{byType: map[string]*ExternalIDLookup{
		"movie": {ByTMDB: map[string]string{"1": "movie-a"}},
	}}
	fetcher := &findingFetcher{}
	entries := []TMDBCollectionEntry{{ID: 1, MediaType: "movie"}, {ID: 2, MediaType: "movie"}, {ID: 3, MediaType: "tv"}}

	got, err := MatchTMDBEntries(t.Context(), items, fetcher, entries)
	if err != nil {
		t.Fatalf("MatchTMDBEntries: %v", err)
	}
	if !slices.Equal(got, []string{"movie-a", "", ""}) {
		t.Fatalf("content IDs = %v", got)
	}
	if len(fetcher.asked) != 0 || len(fetcher.found) != 0 {
		t.Fatalf("made TMDB requests (enriched %v, found %v) with no title lacking a TMDB ID", fetcher.asked, fetcher.found)
	}
}

func TestMatchTMDBEntriesFindsTitlesWhenFewerThanUnmatchedEntries(t *testing.T) {
	items := &fakeExternalIDResolver{
		byType:      map[string]*ExternalIDLookup{"movie": {ByTMDB: map[string]string{"1": "movie-a"}}},
		withoutTMDB: map[string][]TitleWithoutTMDBID{"movie": {{ContentID: "movie-b", IMDbID: "tt3"}}},
	}
	fetcher := &findingFetcher{}
	entries := []TMDBCollectionEntry{
		{ID: 1, MediaType: "movie"},
		{ID: 2, MediaType: "movie"},
		{ID: 3, MediaType: "movie"}, // the title the catalog holds by IMDb ID only
		{ID: 4, MediaType: "movie"},
		{ID: 5, MediaType: "person"},
	}

	got, err := MatchTMDBEntries(t.Context(), items, fetcher, entries)
	if err != nil {
		t.Fatalf("MatchTMDBEntries: %v", err)
	}
	if want := []string{"movie-a", "", "movie-b", "", ""}; !slices.Equal(got, want) {
		t.Fatalf("content IDs = %v, want %v", got, want)
	}
	if !slices.Equal(fetcher.found, []string{"movie-b"}) || len(fetcher.asked) != 0 {
		t.Fatalf("found %v and enriched %v; want one title found and no entry enriched", fetcher.found, fetcher.asked)
	}
}

func TestMatchTMDBEntriesEnrichesEntriesWhenFewerThanTitlesWithoutTMDBID(t *testing.T) {
	items := &fakeExternalIDResolver{
		byType: map[string]*ExternalIDLookup{"movie": {ByIMDb: map[string]string{"tt2": "movie-b"}}},
		withoutTMDB: map[string][]TitleWithoutTMDBID{"movie": {
			{ContentID: "movie-b", IMDbID: "tt2"}, {ContentID: "movie-x", IMDbID: "tt8"}, {ContentID: "movie-y", IMDbID: "tt9"},
		}},
	}
	fetcher := &findingFetcher{}
	entries := []TMDBCollectionEntry{{ID: 2, MediaType: "movie"}, {ID: 4, MediaType: "movie"}}

	got, err := MatchTMDBEntries(t.Context(), items, fetcher, entries)
	if err != nil {
		t.Fatalf("MatchTMDBEntries: %v", err)
	}
	if !slices.Equal(got, []string{"movie-b", ""}) {
		t.Fatalf("content IDs = %v", got)
	}
	if !slices.Equal(fetcher.asked, []int{2, 4}) || len(fetcher.found) != 0 {
		t.Fatalf("enriched %v and found %v; want the two entries enriched and no title found", fetcher.asked, fetcher.found)
	}
	if entries[0].IMDbID != "tt2" {
		t.Fatalf("enriched IMDb ID not written back: %+v", entries[0])
	}
}

func TestMatchTMDBEntriesWithoutFinderEnrichesUnmatchedEntries(t *testing.T) {
	items := &fakeExternalIDResolver{
		byType:      map[string]*ExternalIDLookup{"series": {ByIMDb: map[string]string{"tt7": "series-a"}}},
		withoutTMDB: map[string][]TitleWithoutTMDBID{"series": {{ContentID: "series-a", IMDbID: "tt7"}}},
	}
	fetcher := &enrichingFetcher{}
	got, err := MatchTMDBEntries(t.Context(), items, fetcher, []TMDBCollectionEntry{{ID: 7, MediaType: "tv"}, {ID: 8, MediaType: "tv"}})
	if err != nil {
		t.Fatalf("MatchTMDBEntries: %v", err)
	}
	if !slices.Equal(got, []string{"series-a", ""}) || !slices.Equal(fetcher.asked, []int{7, 8}) {
		t.Fatalf("got %v after enriching %v", got, fetcher.asked)
	}
}

func TestMatchTMDBEntriesReturnsEnrichmentError(t *testing.T) {
	items := &fakeExternalIDResolver{withoutTMDB: map[string][]TitleWithoutTMDBID{"movie": {{ContentID: "m", IMDbID: "tt1"}, {ContentID: "n", IMDbID: "tt2"}}}}
	fetcher := &enrichingFetcher{err: context.Canceled}
	if _, err := MatchTMDBEntries(t.Context(), items, fetcher, []TMDBCollectionEntry{{ID: 1, MediaType: "movie"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestExternalIDLookupResolveFollowsAnchorPrecedence(t *testing.T) {
	lookup := &ExternalIDLookup{
		ByTMDB: map[string]string{"1": "by-tmdb"},
		ByIMDb: map[string]string{"tt1": "by-imdb"},
		ByTVDB: map[string]string{"9": "by-tvdb"},
	}
	for _, tc := range []struct {
		itemType, tvdb, tmdb, imdb, want string
	}{
		{playableTypeSeries, "9", "1", "tt1", "by-tvdb"},
		{playableTypeSeries, "", "1", "tt1", "by-tmdb"},
		{playableTypeMovie, "9", "1", "tt1", "by-tmdb"},
		{playableTypeMovie, "9", "", "tt1", "by-imdb"},
		{playableTypeMovie, "9", "2", "tt2", "by-tvdb"},
		{playableTypeMovie, "8", "2", "tt2", ""},
	} {
		if got := lookup.Resolve(tc.itemType, tc.tvdb, tc.tmdb, tc.imdb); got != tc.want {
			t.Errorf("Resolve(%q, %q, %q, %q) = %q, want %q", tc.itemType, tc.tvdb, tc.tmdb, tc.imdb, got, tc.want)
		}
	}
	if got := (*ExternalIDLookup)(nil).Resolve(playableTypeMovie, "9", "1", "tt1"); got != "" {
		t.Errorf("nil lookup resolved %q", got)
	}
}
