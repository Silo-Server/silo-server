package trakt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetCollectionPresetTrendingSendsHeadersAndDecodesMovies(t *testing.T) {
	var gotAPIKey, gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("trakt-api-key")
		gotVersion = r.Header.Get("trakt-api-version")
		if r.URL.Path != "/movies/trending" {
			t.Fatalf("path = %s, want /movies/trending", r.URL.Path)
		}
		writeJSON(t, w, []map[string]any{{
			"watchers": 3,
			"movie": map[string]any{
				"title": "The Matrix",
				"year":  1999,
				"ids": map[string]any{
					"trakt": 1,
					"tmdb":  603,
					"imdb":  "tt0133093",
				},
			},
		}})
	}))
	defer server.Close()

	client := NewClient("client-id", 1000)
	client.SetBaseURL(server.URL)

	results, err := client.GetCollectionPreset(context.Background(), "trending", "movie", 1, "")
	if err != nil {
		t.Fatalf("GetCollectionPreset: %v", err)
	}
	if gotAPIKey != "client-id" || gotVersion != "2" {
		t.Fatalf("headers api=%q version=%q", gotAPIKey, gotVersion)
	}
	if len(results) != 1 || results[0].Title != "The Matrix" || results[0].TMDBID != 603 || results[0].IMDbID != "tt0133093" {
		t.Fatalf("results = %+v", results)
	}
}

func TestGetCollectionPresetRecommendedUsesBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/recommendations/shows" {
			t.Fatalf("path = %s, want /recommendations/shows", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q", got)
		}
		writeJSON(t, w, []map[string]any{{
			"title": "The Expanse",
			"year":  2015,
			"ids": map[string]any{
				"trakt": 2,
				"tvdb":  280619,
				"tmdb":  63639,
				"imdb":  "tt3230854",
			},
		}})
	}))
	defer server.Close()

	client := NewClient("client-id", 1000)
	client.SetBaseURL(server.URL)

	results, err := client.GetCollectionPreset(context.Background(), "recommended", "tv", 1, "token")
	if err != nil {
		t.Fatalf("GetCollectionPreset: %v", err)
	}
	if len(results) != 1 || results[0].MediaType != "tv" || results[0].TVDBID != 280619 {
		t.Fatalf("results = %+v", results)
	}
}

func TestGetCollectionPresetRejectsRecommendedWithoutToken(t *testing.T) {
	client := NewClient("client-id", 1000)
	if _, err := client.GetCollectionPreset(context.Background(), "recommended", "movie", 1, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetCollectionPresetRetriesRateLimit(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeJSON(t, w, []map[string]any{{
			"title": "Popular",
			"year":  2026,
			"ids": map[string]any{
				"trakt": 3,
				"tmdb":  10,
			},
		}})
	}))
	defer server.Close()

	client := NewClient("client-id", 1000)
	client.SetBaseURL(server.URL)

	results, err := client.GetCollectionPreset(context.Background(), "popular", "movie", 1, "")
	if err != nil {
		t.Fatalf("GetCollectionPreset: %v", err)
	}
	if attempts != 2 || len(results) != 1 {
		t.Fatalf("attempts=%d results=%+v", attempts, results)
	}
}

// A saved credential change has to reach a client that was built at startup;
// this is what lets watchsync.trakt.client_id stay out of the restart registry.
func TestSetClientIDAppliesToLaterRequests(t *testing.T) {
	var gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("trakt-api-key")
		writeJSON(t, w, []map[string]any{})
	}))
	defer server.Close()

	client := NewClient("", 1000)
	client.SetBaseURL(server.URL)

	if _, err := client.GetCollectionPreset(context.Background(), "popular", "movie", 1, ""); err == nil {
		t.Fatal("expected an error while no client id is configured")
	}

	client.SetClientID("  rotated-client-id  ")
	if got := client.ClientID(); got != "rotated-client-id" {
		t.Fatalf("ClientID() = %q, want trimmed rotated-client-id", got)
	}
	if _, err := client.GetCollectionPreset(context.Background(), "popular", "movie", 1, ""); err != nil {
		t.Fatalf("GetCollectionPreset after SetClientID: %v", err)
	}
	if gotAPIKey != "rotated-client-id" {
		t.Fatalf("trakt-api-key = %q, want rotated-client-id", gotAPIKey)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
