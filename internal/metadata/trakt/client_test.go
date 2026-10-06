package trakt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestGetCollectionPresetTrendingSendsHeadersAndDecodesMovies(t *testing.T) {
	var gotAPIKey, gotVersion, gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("trakt-api-key")
		gotVersion = r.Header.Get("trakt-api-version")
		gotUserAgent = r.Header.Get("User-Agent")
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
	// Trakt may block requests without an identifying User-Agent.
	if !strings.HasPrefix(gotUserAgent, "Silo/") {
		t.Fatalf("User-Agent = %q, want Silo/<build>", gotUserAgent)
	}
	if len(results) != 1 || results[0].Title != "The Matrix" || results[0].TMDBID != 603 || results[0].IMDbID != "tt0133093" {
		t.Fatalf("results = %+v", results)
	}
}

// Recommendations take a limit of at most 100 and no page, so the preset asks
// once instead of walking pages.
func TestGetCollectionPresetRecommendedUsesBearerToken(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
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

	results, err := client.GetCollectionPreset(context.Background(), "recommended", "tv", 250, "token")
	if err != nil {
		t.Fatalf("GetCollectionPreset: %v", err)
	}
	if len(queries) != 1 || queries[0] != "limit=100" {
		t.Fatalf("queries = %q, want one request with limit=100", queries)
	}
	if len(results) != 1 || results[0].MediaType != "tv" || results[0].TVDBID != 280619 || results[0].Rank != 1 {
		t.Fatalf("results = %+v", results)
	}
}

func TestGetCollectionPresetRejectsRecommendedWithoutToken(t *testing.T) {
	client := NewClient("client-id", 1000)
	if _, err := client.GetCollectionPreset(context.Background(), "recommended", "movie", 1, ""); err == nil {
		t.Fatal("expected error")
	}
}

type retryTransport func(*http.Request) (*http.Response, error)

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGetCollectionPresetRetriesRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		client := NewClient("client-id", 1000)
		client.httpClient = &http.Client{Transport: retryTransport(func(*http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"1"}}, Body: http.NoBody}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`[{"title":"Popular","year":2026,"ids":{"trakt":3,"tmdb":10}}]`))}, nil
		})}
		started := time.Now()
		results, err := client.GetCollectionPreset(t.Context(), "popular", "movie", 1, "")
		if err != nil {
			t.Fatalf("GetCollectionPreset: %v", err)
		}
		if attempts != 2 || len(results) != 1 {
			t.Fatalf("attempts=%d results=%+v", attempts, results)
		}
		if elapsed := time.Since(started); elapsed < time.Second {
			t.Fatalf("retry waited %s, want at least the Retry-After second", elapsed)
		}
	})
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
