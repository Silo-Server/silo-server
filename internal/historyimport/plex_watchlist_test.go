package historyimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizePlexWatchlistItem(t *testing.T) {
	movie := PlexItem{
		RatingKey: "wl-1",
		Type:      "movie",
		Title:     "Dune: Part Two",
		Year:      2024,
		Guid:      PlexGuids{{ID: "imdb://tt15239678"}, {ID: "tmdb://693134"}},
	}
	record := NormalizePlexWatchlistItem(movie)
	if record.Kind != KindMovie {
		t.Fatalf("movie kind = %q, want %q", record.Kind, KindMovie)
	}
	if !record.Watchlisted {
		t.Fatal("watchlist record must be flagged Watchlisted")
	}
	if record.Played || record.PlayCount != 0 || record.LastPlayedAt != nil {
		t.Fatalf("watchlist record must carry no watch state: %+v", record)
	}
	if record.IMDbID != "tt15239678" || record.TMDBID != "693134" {
		t.Fatalf("ids = imdb %q tmdb %q, want parsed from guids", record.IMDbID, record.TMDBID)
	}

	show := PlexItem{
		RatingKey: "wl-2",
		Type:      "show",
		Title:     "Severance",
		Year:      2022,
		Guid:      PlexGuids{{ID: "tvdb://371980"}},
	}
	record = NormalizePlexWatchlistItem(show)
	if record.Kind != KindSeries {
		t.Fatalf("show kind = %q, want %q (Plex watchlist uses 'show')", record.Kind, KindSeries)
	}
	if record.TVDBID != "371980" {
		t.Fatalf("tvdb id = %q, want parsed", record.TVDBID)
	}
}

func TestFetchWatchlistPaginatesDiscoverAPI(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections/watchlist/all" {
			t.Errorf("path = %q, want /library/sections/watchlist/all", r.URL.Path)
		}
		gotToken = r.Header.Get("X-Plex-Token")
		start := r.URL.Query().Get("X-Plex-Container-Start")
		page := map[string]any{}
		if start == "0" {
			page = map[string]any{"MediaContainer": map[string]any{
				"totalSize": 2,
				"Metadata": []map[string]any{{
					"ratingKey": "wl-1", "type": "movie", "title": "Dune: Part Two", "year": 2024,
					"Guid": []map[string]string{{"id": "tmdb://693134"}},
				}},
			}}
		} else {
			page = map[string]any{"MediaContainer": map[string]any{
				"totalSize": 2,
				"Metadata": []map[string]any{{
					"ratingKey": "wl-2", "type": "show", "title": "Severance", "year": 2022,
					"Guid": []map[string]string{{"id": "tvdb://371980"}},
				}},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(page); err != nil {
			t.Errorf("encode page: %v", err)
		}
	}))
	defer server.Close()

	client := NewPlexClient()
	client.discoverBaseURL = server.URL
	items, err := client.FetchWatchlist(context.Background(), "account-token-1")
	if err != nil {
		t.Fatalf("FetchWatchlist: %v", err)
	}
	if gotToken != "account-token-1" {
		t.Fatalf("token header = %q, want account token", gotToken)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (paginated)", len(items))
	}
	if items[0].RatingKey != "wl-1" || items[1].RatingKey != "wl-2" {
		t.Fatalf("unexpected items: %+v", items)
	}
}

// Guard against page-size regressions: a server reporting a huge total but
// returning empty pages must not loop forever.
func TestFetchWatchlistStopsOnEmptyPage(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"MediaContainer":{"totalSize":999,"Metadata":[]}}`)
	}))
	defer server.Close()

	client := NewPlexClient()
	client.discoverBaseURL = server.URL
	items, err := client.FetchWatchlist(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchWatchlist: %v", err)
	}
	if len(items) != 0 || calls != 1 {
		t.Fatalf("items=%d calls=%d, want 0 items after a single call", len(items), calls)
	}
}
