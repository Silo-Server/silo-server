package historyimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeJellyfinItem_Episode(t *testing.T) {
	series := jellyfinItem{ID: "s1", Name: "The Series", ProductionYear: 2014, ProviderIDs: map[string]string{"tmdb": "100"}}
	item := jellyfinItem{ID: "e1", Type: "Episode", Name: "Pilot", SeriesName: "The Series", SeriesID: "s1", ProductionYear: 2015, RunTimeTicks: 3_000_000_000, UserData: jellyfinUserData{PlaybackPositionTicks: 90_000_000, PlayCount: 2, Played: true}, IndexNumber: 1, ParentIndexNumber: 1, ProviderIDs: map[string]string{"imdb": "tt1"}}
	record := normalizeJellyfinItem(item, series)
	if record.Kind != KindEpisode || record.SeriesTitle != "The Series" || record.SeriesYear != 2014 || record.SeriesTMDBID != "100" || record.IMDbID != "tt1" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.SeasonNumber != 1 || record.EpisodeNumber != 1 {
		t.Fatalf("unexpected episode numbers: %+v", record)
	}
	if record.PositionSeconds < 9 || record.PositionSeconds > 9.1 {
		t.Fatalf("position seconds = %f", record.PositionSeconds)
	}
}

// Without a LastPlayedDate the record must not claim to be newer than local
// activity: a zero UpdatedAt sorts before any Silo progress on re-import.
func TestNormalizeJellyfinItem_MissingLastPlayedDateLeavesUpdatedAtZero(t *testing.T) {
	record := normalizeJellyfinItem(jellyfinItem{ID: "m1", Type: "Movie", UserData: jellyfinUserData{Played: true}}, jellyfinItem{})
	if !record.UpdatedAt.IsZero() || record.LastPlayedAt != nil {
		t.Fatalf("UpdatedAt = %v, LastPlayedAt = %v; want zero and nil", record.UpdatedAt, record.LastPlayedAt)
	}
}

func TestJellyfinProviderFetch_MergesDuplicates(t *testing.T) {
	first := Record{ExternalID: "1", Kind: KindMovie, Title: "Movie", Played: false, PlayCount: 1, PositionSeconds: 10, DurationSeconds: 100, UpdatedAt: time.Unix(1, 0).UTC()}
	second := Record{ExternalID: "1", Kind: KindMovie, Title: "Movie", Played: true, PlayCount: 2, PositionSeconds: 20, DurationSeconds: 120, UpdatedAt: time.Unix(2, 0).UTC()}
	merged := mergeRecords(first, second)
	if !merged.Played || merged.PlayCount != 2 || merged.PositionSeconds != 20 || merged.DurationSeconds != 120 || !merged.UpdatedAt.Equal(time.Unix(2, 0).UTC()) {
		t.Fatalf("unexpected merged record: %+v", merged)
	}
}

func TestJellyfinProviderFetch_ImportsFavorites(t *testing.T) {
	t.Parallel()

	lastPlayed := time.Date(2024, 3, 1, 20, 0, 0, 0, time.UTC)
	server := newJellyfinFetchServer(t, map[string][]jellyfinItem{
		"IsPlayed": {
			{ID: "heat", Type: "Movie", Name: "Heat", ProviderIDs: map[string]string{"Tmdb": "949"}, UserData: jellyfinUserData{Played: true, PlayCount: 1, LastPlayedDate: &lastPlayed, IsFavorite: true}},
		},
		"IsFavorite": {
			{ID: "heat", Type: "Movie", Name: "Heat", ProviderIDs: map[string]string{"Tmdb": "949"}, UserData: jellyfinUserData{Played: true, IsFavorite: true}},
			{ID: "arrival", Type: "Movie", Name: "Arrival", ProviderIDs: map[string]string{"Tmdb": "329865"}, UserData: jellyfinUserData{IsFavorite: true}},
			{ID: "office", Type: "Series", Name: "The Office", ProviderIDs: map[string]string{"Tvdb": "73244"}, UserData: jellyfinUserData{IsFavorite: true}},
			{ID: "bb-s2e2", Type: "Episode", Name: "Grilled", SeriesID: "bb", ParentIndexNumber: 2, IndexNumber: 2, UserData: jellyfinUserData{IsFavorite: true}},
		},
	}, map[string]jellyfinItem{
		"bb": {ID: "bb", Type: "Series", Name: "Breaking Bad", ProductionYear: 2008, ProviderIDs: map[string]string{"Tvdb": "81189"}},
	})

	records, warnings, err := NewJellyfinProvider(NewJellyfinClient(), jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	byID := map[string]Record{}
	for _, record := range records {
		byID[record.ExternalID] = record
	}
	if len(byID) != 4 {
		t.Fatalf("records = %+v, want heat, arrival, office, bb-s2e2", records)
	}
	if heat := byID["heat"]; !heat.Played || !heat.Favorite || heat.FavoriteOnly || heat.LastPlayedAt == nil {
		t.Fatalf("played favorite = %+v, want played favorite with watch state", heat)
	}
	if arrival := byID["arrival"]; !arrival.Favorite || !arrival.FavoriteOnly || arrival.Kind != KindMovie || arrival.TMDBID != "329865" {
		t.Fatalf("favorite movie = %+v", arrival)
	}
	if office := byID["office"]; !office.FavoriteOnly || office.Kind != KindSeries || office.TVDBID != "73244" {
		t.Fatalf("favorite series = %+v", office)
	}
	if episode := byID["bb-s2e2"]; !episode.FavoriteOnly || episode.Kind != KindEpisode || episode.SeriesTVDBID != "81189" || episode.SeriesTitle != "Breaking Bad" || episode.SeasonNumber != 2 || episode.EpisodeNumber != 2 {
		t.Fatalf("favorite episode = %+v, want series identity for S02E02", episode)
	}
}

// Favorites are secondary: a failed favorites query must not discard the
// watch history already fetched.
func TestJellyfinProviderFetch_FavoritesFailureIsAWarning(t *testing.T) {
	t.Parallel()

	server := newJellyfinFetchServer(t, map[string][]jellyfinItem{
		"IsPlayed": {{ID: "matrix", Type: "Movie", Name: "The Matrix", ProviderIDs: map[string]string{"Tmdb": "603"}, UserData: jellyfinUserData{Played: true}}},
	}, nil)

	records, warnings, err := NewJellyfinProvider(NewJellyfinClient(), jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(records) != 1 || records[0].ExternalID != "matrix" {
		t.Fatalf("records = %+v", records)
	}
	if len(warnings) != 1 || warnings[0] != warnJellyfinFavoritesUnavailable {
		t.Fatalf("warnings = %v, want only %q", warnings, warnJellyfinFavoritesUnavailable)
	}
}

// A favorite's series lookup is as optional as the favorites query: when it
// fails, the watch history and the favorite episode survive with a warning.
func TestJellyfinProviderFetch_FavoriteSeriesLookupFailureIsAWarning(t *testing.T) {
	t.Parallel()

	server := newJellyfinFetchServer(t, map[string][]jellyfinItem{
		"IsPlayed":   {{ID: "matrix", Type: "Movie", Name: "The Matrix", ProviderIDs: map[string]string{"Tmdb": "603"}, UserData: jellyfinUserData{Played: true}}},
		"IsFavorite": {{ID: "bb-s2e2", Type: "Episode", Name: "Grilled", SeriesID: "bb", ParentIndexNumber: 2, IndexNumber: 2, ProviderIDs: map[string]string{"Tvdb": "349234"}, UserData: jellyfinUserData{IsFavorite: true}}},
	}, nil)

	records, warnings, err := NewJellyfinProvider(NewJellyfinClient(), jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	byID := map[string]Record{}
	for _, record := range records {
		byID[record.ExternalID] = record
	}
	if matrix := byID["matrix"]; len(byID) != 2 || !matrix.Played {
		t.Fatalf("records = %+v, want the played movie and the favorite episode", records)
	}
	if episode := byID["bb-s2e2"]; !episode.FavoriteOnly || episode.TVDBID != "349234" {
		t.Fatalf("favorite episode = %+v, want favorite-only with its own TVDB ID", episode)
	}
	if len(warnings) != 1 || warnings[0] != warnJellyfinFavoriteSeriesUnavailable {
		t.Fatalf("warnings = %v, want only %q", warnings, warnJellyfinFavoriteSeriesUnavailable)
	}
}

// newJellyfinFetchServer serves /Items by filter (a filter absent from
// byFilter answers 500), an empty /UserItems/Resume, and /Items?Ids= lookups
// from byID (a nil byID answers 500).
func newJellyfinFetchServer(t *testing.T, byFilter map[string][]jellyfinItem, byID map[string]jellyfinItem) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertJellyfinAuthorization(t, r, "token-1")
		query := r.URL.Query()
		var items []jellyfinItem
		switch {
		case r.URL.Path == "/UserItems/Resume":
		case r.URL.Path == "/Items" && query.Get("Ids") != "":
			if byID == nil {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			for id := range strings.SplitSeq(query.Get("Ids"), ",") {
				if item, ok := byID[id]; ok {
					items = append(items, item)
				}
			}
		case r.URL.Path == "/Items":
			if query.Get("UserId") != "user-1" {
				t.Errorf("UserId = %q, want user-1", query.Get("UserId"))
			}
			found, ok := byFilter[query.Get("Filters")]
			if !ok {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			if query.Get("Filters") == "IsFavorite" && query.Get("IncludeItemTypes") != "Movie,Series,Episode" {
				t.Errorf("favorite IncludeItemTypes = %q", query.Get("IncludeItemTypes"))
			}
			items = found
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: items, TotalRecordCount: len(items)})
	}))
	t.Cleanup(server.Close)
	return server
}
