package historyimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlexAdminProviderFetch(t *testing.T) {
	ctx := context.Background()

	// Create a test server to mock the Plex and Discover APIs.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/user":
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("X-Plex-Token") != "admin-token" {
				t.Errorf("expected X-Plex-Token admin-token, got %q", r.Header.Get("X-Plex-Token"))
			}
			resp := map[string]any{
				"id":   99999,
				"uuid": "admin-user-uuid",
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/api/v2/friends":
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("X-Plex-Token") != "admin-token" {
				t.Errorf("expected X-Plex-Token admin-token, got %q", r.Header.Get("X-Plex-Token"))
			}
			resp := []map[string]any{
				{
					"id":   12345,
					"uuid": "resolved-friend-uuid",
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/status/sessions/history/all":
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("X-Plex-Token") != "admin-token" {
				t.Errorf("expected X-Plex-Token admin-token, got %q", r.Header.Get("X-Plex-Token"))
			}
			if r.URL.Query().Get("accountID") != "12345" {
				t.Errorf("expected accountID 12345, got %q", r.URL.Query().Get("accountID"))
			}
			resp := map[string]any{
				"MediaContainer": map[string]any{
					"size":      1,
					"totalSize": 1,
					"Metadata": []map[string]any{
						{
							"ratingKey":    "history-movie-1",
							"type":         "movie",
							"title":        "Inception",
							"year":         2010,
							"viewCount":    1,
							"lastViewedAt": 1756839905,
							"Guid": []map[string]string{
								{"id": "tmdb://27205"},
							},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/api":
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"data": map[string]any{
					"userV2": map[string]any{
						"watchlist": map[string]any{
							"nodes": []map[string]any{
								{
									"id":    "watchlist-item-1",
									"title": "Interstellar (2014)",
									"type":  "MOVIE",
									"guid":  "plex://movie/watchlist-item-1",
								},
							},
							"pageInfo": map[string]any{
								"hasNextPage": false,
								"endCursor":   "",
							},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/library/metadata/watchlist-item-1":
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"MediaContainer": map[string]any{
					"Metadata": []map[string]any{
						{
							"ratingKey": "watchlist-item-1",
							"type":      "movie",
							"title":     "Interstellar",
							"year":      2014,
							"Guid": []map[string]string{
								{"id": "tmdb://157336"},
							},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewPlexClient()
	client.discoverBaseURL = server.URL
	client.tvBaseURL = server.URL
	client.communityBaseURL = server.URL

	provider := NewPlexAdminProvider(client, server.URL, "admin-token", "12345")
	records, warnings, err := provider.Fetch(ctx)
	if err != nil {
		t.Fatalf("provider.Fetch failed: %v", err)
	}

	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d: %+v", len(records), records)
	}

	// Verify history record
	historyFound := false
	watchlistFound := false
	for _, record := range records {
		switch record.ExternalID {
		case "history-movie-1":
			historyFound = true
			if record.Title != "Inception" {
				t.Errorf("expected history movie title 'Inception', got %q", record.Title)
			}
			if !record.Played {
				t.Errorf("expected history record to be played")
			}
			if record.Watchlisted {
				t.Errorf("expected history record not to be flagged watchlisted")
			}
		case "watchlist-item-1":
			watchlistFound = true
			if record.Title != "Interstellar" {
				t.Errorf("expected watchlist movie title 'Interstellar', got %q", record.Title)
			}
			if record.Year != 2014 {
				t.Errorf("expected watchlist movie year 2014, got %d", record.Year)
			}
			if record.Played {
				t.Errorf("expected watchlist record not to be played")
			}
			if !record.Watchlisted {
				t.Errorf("expected watchlist record to be flagged watchlisted")
			}
			if record.TMDBID != "157336" {
				t.Errorf("expected TMDB ID '157336', got %q", record.TMDBID)
			}
		}
	}

	if !historyFound {
		t.Error("history record not found in fetched records")
	}
	if !watchlistFound {
		t.Error("watchlist record not found in fetched records")
	}
}

func TestPlexAdminProviderFetchWatchlistFallback(t *testing.T) {
	ctx := context.Background()

	// Mock server that returns history successfully, but fails the watchlist call with 500.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/user":
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"id":   99999,
				"uuid": "admin-user-uuid",
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/api/v2/friends":
			w.Header().Set("Content-Type", "application/json")
			resp := []map[string]any{
				{
					"id":   12345,
					"uuid": "resolved-friend-uuid",
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/status/sessions/history/all":
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"MediaContainer": map[string]any{
					"size":      1,
					"totalSize": 1,
					"Metadata": []map[string]any{
						{
							"ratingKey":    "history-movie-1",
							"type":         "movie",
							"title":        "Inception",
							"year":         2010,
							"viewCount":    1,
							"lastViewedAt": 1756839905,
							"Guid": []map[string]string{
								{"id": "tmdb://27205"},
							},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/api":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error": "Internal Server Error"}`)

		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewPlexClient()
	client.discoverBaseURL = server.URL
	client.tvBaseURL = server.URL
	client.communityBaseURL = server.URL

	provider := NewPlexAdminProvider(client, server.URL, "admin-token", "12345")
	records, warnings, err := provider.Fetch(ctx)
	if err != nil {
		t.Fatalf("provider.Fetch failed: %v", err)
	}

	// We expect 1 warning from the failed watchlist fetch
	if len(warnings) != 1 {
		t.Errorf("expected exactly 1 warning, got %d: %v", len(warnings), warnings)
	}

	// We expect 1 record from history
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d: %+v", len(records), records)
	}

	if records[0].ExternalID != "history-movie-1" {
		t.Errorf("expected record external ID 'history-movie-1', got %q", records[0].ExternalID)
	}
}
