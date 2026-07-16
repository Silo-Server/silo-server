package historyimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbyProviderFetchIncludesMovieAndSeriesFavorites(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("EnableUserData"); got != "true" {
			t.Errorf("EnableUserData = %q, want true", got)
		}
		var items []embyItem
		switch filter := r.URL.Query().Get("Filters"); filter {
		case "IsPlayed":
			items = []embyItem{{
				ID: "movie-1", Name: "Arrival", Type: "Movie", ProductionYear: 2016,
				ProviderIDs: map[string]string{"Tmdb": "329865"},
			}}
		case "IsResumable":
			items = []embyItem{}
		case "IsFavorite":
			if got := r.URL.Query().Get("IncludeItemTypes"); got != "Movie,Series" {
				t.Errorf("favorite IncludeItemTypes = %q, want Movie,Series", got)
			}
			items = []embyItem{
				{ID: "movie-1", Name: "Arrival", Type: "Movie", ProductionYear: 2016, ProviderIDs: map[string]string{"Tmdb": "329865"}},
				{ID: "series-1", Name: "Severance", Type: "Series", ProductionYear: 2022, ProviderIDs: map[string]string{"Tvdb": "371980"}},
			}
		default:
			t.Errorf("unexpected filter %q", filter)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(embyItemsResponse{Items: items}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	provider := NewEmbyProvider(NewEmbyClient(), embyLocalAuth{
		BaseURL: server.URL, UserID: "emby-user", AccessToken: "token",
	})
	records, warnings, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}

	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.ExternalID] = record
	}
	movie := byID["movie-1"]
	if !movie.Favorite || movie.FavoriteOnly {
		t.Fatalf("merged movie flags = favorite:%v favoriteOnly:%v, want true/false", movie.Favorite, movie.FavoriteOnly)
	}
	if !movie.PreferTMDB {
		t.Fatalf("movie PreferTMDB = %v, want true", movie.PreferTMDB)
	}
	series := byID["series-1"]
	if series.Kind != KindSeries || !series.Favorite || !series.FavoriteOnly {
		t.Fatalf("series record = %+v, want favorite-only series", series)
	}
	if !series.PreferTMDB {
		t.Fatalf("series PreferTMDB = %v, want true", series.PreferTMDB)
	}
}

func TestEmbyProviderFetchContinuesWhenFavoritesFail(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("Filters") {
		case "IsPlayed":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(embyItemsResponse{Items: []embyItem{{
				ID: "movie-1", Name: "Arrival", Type: "Movie", ProductionYear: 2016,
				ProviderIDs: map[string]string{"Tmdb": "329865"},
			}}}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		case "IsResumable":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(embyItemsResponse{}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		case "IsFavorite":
			http.Error(w, "favorites unavailable", http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	provider := NewEmbyProvider(NewEmbyClient(), embyLocalAuth{
		BaseURL: server.URL, UserID: "emby-user", AccessToken: "token",
	})
	records, warnings, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(records) != 1 || records[0].ExternalID != "movie-1" {
		t.Fatalf("records = %+v, want played movie preserved", records)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one favorites warning", warnings)
	}
}

func TestNormalizeEmbyItemWithoutLastPlayedDateHasNoFreshnessTimestamp(t *testing.T) {
	t.Parallel()

	item := embyItem{
		ID:           "emby-episode-1",
		Name:         "Stale partial",
		Type:         "Episode",
		SeriesName:   "The Show",
		SeriesID:     "emby-series-1",
		RunTimeTicks: 3_000_000_000,
	}
	item.UserData.PlaybackPositionTicks = 1_200_000_000

	record := normalizeEmbyItem(item, embyItem{Name: "The Show"})

	if !record.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt = %v, want zero for resumable without LastPlayedDate", record.UpdatedAt)
	}
	if record.PositionSeconds != 120 {
		t.Fatalf("PositionSeconds = %v, want 120", record.PositionSeconds)
	}
}

func TestNormalizeEmbyItemUsesLastPlayedDateForFreshness(t *testing.T) {
	t.Parallel()

	lastPlayed := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	item := embyItem{ID: "emby-episode-1", Name: "Played", Type: "Episode"}
	item.UserData.LastPlayedDate = &lastPlayed
	item.UserData.Played = true

	record := normalizeEmbyItem(item, embyItem{})

	if !record.UpdatedAt.Equal(lastPlayed) {
		t.Fatalf("UpdatedAt = %v, want %v", record.UpdatedAt, lastPlayed)
	}
	if record.LastPlayedAt == nil || !record.LastPlayedAt.Equal(lastPlayed) {
		t.Fatalf("LastPlayedAt = %v, want %v", record.LastPlayedAt, lastPlayed)
	}
}
