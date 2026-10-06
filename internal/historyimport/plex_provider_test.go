package historyimport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParsePlexGuids(t *testing.T) {
	tests := []struct {
		name                         string
		guids                        PlexGuids
		wantIMDb, wantTMDB, wantTVDB string
	}{
		{
			name:     "standard new-style guids",
			guids:    PlexGuids{{ID: "imdb://tt0322259"}, {ID: "tmdb://584"}, {ID: "tvdb://20800"}},
			wantIMDb: "tt0322259", wantTMDB: "584", wantTVDB: "20800",
		},
		{
			name:     "empty guids",
			guids:    nil,
			wantIMDb: "", wantTMDB: "", wantTVDB: "",
		},
		{
			name:     "partial guids",
			guids:    PlexGuids{{ID: "tmdb://12345"}},
			wantIMDb: "", wantTMDB: "12345", wantTVDB: "",
		},
		{
			name:     "case insensitive provider",
			guids:    PlexGuids{{ID: "IMDB://tt9999999"}, {ID: "Tmdb://111"}},
			wantIMDb: "tt9999999", wantTMDB: "111", wantTVDB: "",
		},
		{
			name:     "first value wins on duplicate providers",
			guids:    PlexGuids{{ID: "tmdb://100"}, {ID: "tmdb://200"}},
			wantIMDb: "", wantTMDB: "100", wantTVDB: "",
		},
		{
			name:     "malformed entry ignored",
			guids:    PlexGuids{{ID: "no-separator"}, {ID: "tmdb://999"}},
			wantIMDb: "", wantTMDB: "999", wantTVDB: "",
		},
		{
			name:     "legacy imdb movie agent",
			guids:    PlexGuids{{ID: "com.plexapp.agents.imdb://tt0078748?lang=en"}},
			wantIMDb: "tt0078748", wantTMDB: "", wantTVDB: "",
		},
		{
			name:     "legacy themoviedb series agent",
			guids:    PlexGuids{{ID: "com.plexapp.agents.themoviedb://1396?lang=en"}},
			wantIMDb: "", wantTMDB: "1396", wantTVDB: "",
		},
		{
			name:     "legacy thetvdb series agent",
			guids:    PlexGuids{{ID: "com.plexapp.agents.thetvdb://81189?lang=en"}},
			wantIMDb: "", wantTMDB: "", wantTVDB: "81189",
		},
		{
			name:     "legacy episode guid names the series, not the episode",
			guids:    PlexGuids{{ID: "com.plexapp.agents.thetvdb://81189/1/2?lang=en"}},
			wantIMDb: "", wantTMDB: "", wantTVDB: "",
		},
		{
			name:     "plex and local guids carry no provider id",
			guids:    PlexGuids{{ID: "plex://movie/5d776827880197001ec90904"}, {ID: "local://5001"}},
			wantIMDb: "", wantTMDB: "", wantTVDB: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var imdb, tmdb, tvdb string
			ParsePlexGuids(tt.guids, &imdb, &tmdb, &tvdb)
			if imdb != tt.wantIMDb {
				t.Errorf("imdb = %q, want %q", imdb, tt.wantIMDb)
			}
			if tmdb != tt.wantTMDB {
				t.Errorf("tmdb = %q, want %q", tmdb, tt.wantTMDB)
			}
			if tvdb != tt.wantTVDB {
				t.Errorf("tvdb = %q, want %q", tvdb, tt.wantTVDB)
			}
		})
	}
}

func TestNormalizePlexItem_Movie(t *testing.T) {
	item := PlexItem{
		RatingKey:    "65196",
		Type:         "movie",
		Title:        "2 Fast 2 Furious",
		Year:         2003,
		Duration:     6461788,
		ViewCount:    1,
		LastViewedAt: 1756839905,
		Guid:         PlexGuids{{ID: "tmdb://584"}, {ID: "imdb://tt0322259"}},
	}
	record := NormalizePlexItem(item, nil)

	if record.ExternalID != "65196" {
		t.Errorf("ExternalID = %q, want %q", record.ExternalID, "65196")
	}
	if record.Kind != KindMovie {
		t.Errorf("Kind = %q, want %q", record.Kind, KindMovie)
	}
	if record.Title != "2 Fast 2 Furious" {
		t.Errorf("Title = %q", record.Title)
	}
	if record.Year != 2003 {
		t.Errorf("Year = %d", record.Year)
	}
	if record.TMDBID != "584" {
		t.Errorf("TMDBID = %q", record.TMDBID)
	}
	if record.IMDbID != "tt0322259" {
		t.Errorf("IMDbID = %q", record.IMDbID)
	}
	if !record.Played {
		t.Error("expected Played=true")
	}
	if record.PlayCount != 1 {
		t.Errorf("PlayCount = %d", record.PlayCount)
	}
	if record.DurationSeconds < 6461 || record.DurationSeconds > 6462 {
		t.Errorf("DurationSeconds = %f, want ~6461.788", record.DurationSeconds)
	}
	if record.LastPlayedAt == nil {
		t.Fatal("expected non-nil LastPlayedAt")
	}
	expected := time.Unix(1756839905, 0).UTC()
	if !record.LastPlayedAt.Equal(expected) {
		t.Errorf("LastPlayedAt = %v, want %v", record.LastPlayedAt, expected)
	}
}

func TestNormalizePlexItem_Episode(t *testing.T) {
	series := &PlexItem{
		Title: "Raising Hope",
		Year:  2010,
		Guid:  PlexGuids{{ID: "imdb://tt1615919"}, {ID: "tmdb://32815"}, {ID: "tvdb://164021"}},
	}
	item := PlexItem{
		RatingKey:            "57498",
		Type:                 "episode",
		Title:                "Cheaters",
		GrandparentTitle:     "Raising Hope",
		GrandparentRatingKey: "57479",
		ParentIndex:          1,
		Index:                18,
		Duration:             1297344,
		ViewCount:            2,
		LastViewedAt:         1775013724,
		ViewOffset:           67317,
		Guid:                 PlexGuids{{ID: "imdb://tt1792428"}, {ID: "tmdb://770538"}, {ID: "tvdb://3990621"}},
	}
	record := NormalizePlexItem(item, series)

	if record.Kind != KindEpisode {
		t.Errorf("Kind = %q", record.Kind)
	}
	if record.SeriesTitle != "Raising Hope" {
		t.Errorf("SeriesTitle = %q", record.SeriesTitle)
	}
	if record.SeriesYear != 2010 {
		t.Errorf("SeriesYear = %d", record.SeriesYear)
	}
	if record.SeasonNumber != 1 {
		t.Errorf("SeasonNumber = %d", record.SeasonNumber)
	}
	if record.EpisodeNumber != 18 {
		t.Errorf("EpisodeNumber = %d", record.EpisodeNumber)
	}
	if record.SeriesTMDBID != "32815" {
		t.Errorf("SeriesTMDBID = %q", record.SeriesTMDBID)
	}
	if record.SeriesTVDBID != "164021" {
		t.Errorf("SeriesTVDBID = %q", record.SeriesTVDBID)
	}
	if record.PositionSeconds < 67 || record.PositionSeconds > 68 {
		t.Errorf("PositionSeconds = %f, want ~67.317", record.PositionSeconds)
	}
}

func TestNormalizePlexItem_NoViewCount(t *testing.T) {
	item := PlexItem{
		RatingKey: "100",
		Type:      "movie",
		Title:     "Unwatched Movie",
		ViewCount: 0,
	}
	record := NormalizePlexItem(item, nil)
	if record.Played {
		t.Error("expected Played=false for ViewCount=0")
	}
	if !record.UpdatedAt.IsZero() || record.LastPlayedAt != nil {
		t.Errorf("UpdatedAt = %v, LastPlayedAt = %v; want both unset without a Plex timestamp", record.UpdatedAt, record.LastPlayedAt)
	}
}

// A record without a source timestamp must not claim to be newer than local
// progress: the import's freshness guard treats a zero UpdatedAt as oldest.
func TestNormalizePlexHistoryItem_NoViewedAtLeavesUpdatedAtUnset(t *testing.T) {
	record := NormalizePlexHistoryItem(PlexHistoryItem{RatingKey: "1", Type: "movie", Title: "Heat"}, nil)
	if !record.UpdatedAt.IsZero() || record.LastPlayedAt != nil {
		t.Fatalf("UpdatedAt = %v, LastPlayedAt = %v; want both unset", record.UpdatedAt, record.LastPlayedAt)
	}
}

// The personal import reads watched items and in-progress items per section.
// It must not use /library/onDeck, whose age window drops old resume points
// and whose unstarted next-up episodes carry no watch state.
func TestPlexServerProviderFetchesWatchedAndInProgressItems(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		watched, inProgress := q.Get("unwatched") == "0", q.Get("inProgress") == "1"
		switch {
		case r.URL.Path == "/library/sections":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Directory":[
				{"key":"1","type":"movie","title":"Movies"},
				{"key":"2","type":"show","title":"TV"},
				{"key":"3","type":"artist","title":"Music"}]}}`)
		case r.URL.Path == "/library/sections/1/all" && q.Get("type") == "1" && watched:
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":3,"Metadata":[
				{"ratingKey":"1","type":"movie","title":"Alien","year":1979,"duration":600023,"viewCount":1,"lastViewedAt":1790123003,"Guid":[{"id":"tmdb://348"}]},
				{"ratingKey":"3","type":"movie","title":"Heat","year":1995,"duration":600023,"viewCount":1,"Guid":[{"id":"tmdb://949"}]},
				{"ratingKey":"5","type":"movie","title":"The Matrix","year":1999,"duration":600023,"viewCount":1,"viewOffset":180000,"lastViewedAt":1790123007,"Guid":[{"id":"tmdb://603"}]}]}}`)
		case r.URL.Path == "/library/sections/1/all" && q.Get("type") == "1" && inProgress:
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":2,"Metadata":[
				{"ratingKey":"2","type":"movie","title":"Arrival","year":2016,"duration":600023,"viewOffset":120000,"lastViewedAt":1790123006,"Guid":[{"id":"tmdb://329865"}]},
				{"ratingKey":"5","type":"movie","title":"The Matrix","year":1999,"duration":600023,"viewCount":1,"viewOffset":180000,"lastViewedAt":1790123007,"Guid":[{"id":"tmdb://603"}]}]}}`)
		case r.URL.Path == "/library/sections/2/all" && q.Get("type") == "4" && watched:
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"83","type":"episode","title":"Pilot","grandparentTitle":"Breaking Bad","grandparentRatingKey":"81","parentIndex":1,"index":1,"viewCount":1,"lastViewedAt":1790123008,"guid":"local://83"}]}}`)
		case r.URL.Path == "/library/sections/2/all" && q.Get("type") == "4" && inProgress:
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":2,"Metadata":[
				{"ratingKey":"85","type":"episode","title":"...And the Bag's in the River","grandparentTitle":"Breaking Bad","grandparentRatingKey":"81","parentIndex":1,"index":3,"duration":600023,"viewOffset":240000,"lastViewedAt":1790123059},
				{"ratingKey":"86","type":"episode","title":"Next Up","grandparentTitle":"Breaking Bad","grandparentRatingKey":"81","parentIndex":1,"index":4,"lastViewedAt":1790123059}]}}`)
		case r.URL.Path == "/library/metadata/81":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"81","type":"show","title":"Breaking Bad","year":2008,"Guid":[{"id":"tvdb://81189"}]}]}}`)
		default:
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexServerProvider(newUnthrottledPlexClient(), server.URL, "server-token").Fetch(trustLoopback(context.Background()))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	byKey := map[string]Record{}
	for _, record := range records {
		byKey[record.ExternalID] = record
	}
	if len(records) != 6 {
		t.Fatalf("records = %+v, want Alien, Heat, Matrix, Arrival, and two episodes", records)
	}
	if _, ok := byKey["86"]; ok {
		t.Fatalf("next-up episode without watch state was imported: %+v", byKey["86"])
	}
	if heat := byKey["3"]; !heat.Played || !heat.UpdatedAt.IsZero() {
		t.Fatalf("Heat = %+v, want played with no claimed update time", heat)
	}
	if arrival := byKey["2"]; arrival.Played || arrival.PositionSeconds != 120 {
		t.Fatalf("Arrival = %+v, want an unplayed resume point at 120s", arrival)
	}
	if matrix := byKey["5"]; !matrix.Played {
		t.Fatalf("Matrix = %+v, want played", matrix)
	}
	for _, key := range []string{"83", "85"} {
		episode := byKey[key]
		if episode.Kind != KindEpisode || episode.SeriesTVDBID != "81189" || episode.EpisodeNumber == 0 {
			t.Fatalf("episode %s = %+v, want series identity and coordinates", key, episode)
		}
	}
	if byKey["85"].Played || byKey["85"].PositionSeconds != 240 {
		t.Fatalf("in-progress episode = %+v, want resume point at 240s", byKey["85"])
	}
	for _, path := range paths {
		if strings.Contains(path, "onDeck") || strings.Contains(path, "/library/sections/3/") {
			t.Fatalf("unexpected request %s", path)
		}
	}
}
