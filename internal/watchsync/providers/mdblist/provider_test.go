package mdblist

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestProviderIdentityAndCapabilities(t *testing.T) {
	p := NewProvider(nil, "")
	if p.Key() != "mdblist" {
		t.Fatalf("got key %q, want mdblist", p.Key())
	}
	if p.DisplayName() != "MDBList" {
		t.Fatalf("got display name %q, want MDBList", p.DisplayName())
	}
	caps := p.Capabilities()
	if !caps.ScrobblePlayback || !caps.ImportWatched || !caps.ExportWatchlist {
		t.Fatalf("unexpected capabilities: %#v", caps)
	}
	if caps.ImportFavorites || caps.ExportFavorites || caps.RemoveFavorites {
		t.Fatalf("mdblist should bind to watchlist, not favorites: %#v", caps)
	}
	if !caps.ProvidesWatchlistOrder {
		t.Fatalf("mdblist should provide watchlist order: %#v", caps)
	}
	var _ watchsync.APIKeyAuthProvider = p
}

func TestConnectWithAPIKeyHitsUserEndpoint(t *testing.T) {
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("apikey")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_id":  42,
			"username": "kingsly",
			"name":     "Kingsly Test",
		})
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	tokens, account, err := p.ConnectWithAPIKey(context.Background(), "  test-key  ")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if gotPath != "/user" {
		t.Fatalf("got path %q, want /user", gotPath)
	}
	if gotKey != "test-key" {
		t.Fatalf("got apikey %q, want test-key", gotKey)
	}
	if tokens.AccessToken != "test-key" {
		t.Fatalf("got access token %q, want test-key", tokens.AccessToken)
	}
	if account.ID != "42" || account.Username != "kingsly" {
		t.Fatalf("unexpected account %#v", account)
	}
}

func TestConnectWithAPIKeyRejectsEmpty(t *testing.T) {
	p := NewProvider(http.DefaultClient, "http://127.0.0.1")
	if _, _, err := p.ConnectWithAPIKey(context.Background(), "   "); err == nil {
		t.Fatal("expected empty key to be rejected")
	}
}

func TestFetchWatchedParsesMoviesAndEpisodes(t *testing.T) {
	watched := time.Date(2025, time.October, 21, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"movies": []map[string]any{{
				"watched_at": watched.Format(time.RFC3339),
				"movie": map[string]any{
					"title": "Beetlejuice Beetlejuice",
					"year":  2024,
					"ids":   map[string]any{"imdb": "tt2049403", "tmdb": 917496},
				},
			}},
			"episodes": []map[string]any{{
				"watched_at": watched.Format(time.RFC3339),
				"season":     1,
				"number":     2,
				"title":      "Cat's in the Bag...",
				"ids":        map[string]any{"tvdb": 349231},
				"show": map[string]any{
					"title": "Breaking Bad",
					"year":  2008,
					"ids":   map[string]any{"tvdb": 81189, "imdb": "tt0903747"},
				},
			}},
		})
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	rows, err := p.FetchWatched(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch watched: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Kind != historyimport.KindMovie || rows[0].ProviderItemKey != "imdb:tt2049403" {
		t.Fatalf("unexpected movie row: %#v", rows[0])
	}
	if rows[1].Kind != historyimport.KindEpisode || rows[1].SeasonNumber != 1 || rows[1].EpisodeNumber != 2 {
		t.Fatalf("unexpected episode row: %#v", rows[1])
	}
	if rows[1].SeriesIMDbID != "tt0903747" {
		t.Fatalf("expected series imdb propagated, got %q", rows[1].SeriesIMDbID)
	}
}

func TestFetchProgressParsesFlatArray(t *testing.T) {
	pausedAt := time.Date(2025, time.November, 1, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"progress":  42.5,
				"paused_at": pausedAt.Format(time.RFC3339),
				"action":    "pause",
				"movie": map[string]any{
					"title": "Sample",
					"ids":   map[string]any{"tmdb": 278},
				},
			},
			{
				"progress": 5,
				"action":   "start",
				"movie":    map[string]any{"ids": map[string]any{"tmdb": 999}},
			},
		})
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	rows, err := p.FetchProgress(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch progress: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (paused only)", len(rows))
	}
	if rows[0].ProviderItemKey != "tmdb:278" || rows[0].ProgressPercent != 42.5 {
		t.Fatalf("unexpected row: %#v", rows[0])
	}
	if !rows[0].PausedAt.Equal(pausedAt) {
		t.Fatalf("paused_at mismatch: %v", rows[0].PausedAt)
	}
}

func TestFetchProgressParsesNestedObject(t *testing.T) {
	pausedAt := time.Date(2025, time.November, 1, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"paused": []map[string]any{{
				"progress":  42.5,
				"paused_at": pausedAt.Format(time.RFC3339),
				"movie":     map[string]any{"ids": map[string]any{"tmdb": 278}},
			}},
			"scrobbling": []map[string]any{{
				"progress": 5,
				"movie":    map[string]any{"ids": map[string]any{"tmdb": 999}},
			}},
		})
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	rows, err := p.FetchProgress(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch progress: %v", err)
	}
	if len(rows) != 1 || rows[0].ProviderItemKey != "tmdb:278" {
		t.Fatalf("expected only the paused row imported, got %#v", rows)
	}
}

func TestExportHistorySendsBulkPayload(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("got method %s, want POST", r.Method)
		}
		if r.URL.Path != "/sync/watched" {
			t.Fatalf("got path %q, want /sync/watched", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"added":{},"updated":{},"not_found":{}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	plays := []watchsync.LocalPlay{
		{
			HistoryID: "h1",
			Kind:      historyimport.KindMovie,
			IMDbID:    "tt0111161",
			TMDBID:    "278",
			WatchedAt: time.Date(2025, 10, 21, 12, 0, 0, 0, time.UTC),
		},
		{
			HistoryID:     "h2",
			Kind:          historyimport.KindEpisode,
			SeriesIMDbID:  "tt0903747",
			SeasonNumber:  1,
			EpisodeNumber: 2,
			WatchedAt:     time.Date(2025, 10, 22, 13, 0, 0, 0, time.UTC),
		},
	}
	result, err := p.ExportHistory(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, plays)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(result.Sent) != 2 {
		t.Fatalf("expected 2 sent, got %d", len(result.Sent))
	}
	movies, _ := gotBody["movies"].([]any)
	if len(movies) != 1 {
		t.Fatalf("expected 1 movie in payload, got %v", gotBody["movies"])
	}
	episodes, _ := gotBody["episodes"].([]any)
	if len(episodes) != 1 {
		t.Fatalf("expected 1 episode in payload, got %v", gotBody["episodes"])
	}
}

func TestScrobbleStartUsesEpisodeShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	err := p.Start(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, watchsync.ScrobbleEvent{
		Kind:            historyimport.KindEpisode,
		SeriesIMDbID:    "tt0903747",
		SeasonNumber:    3,
		EpisodeNumber:   7,
		PositionSeconds: 600,
		DurationSeconds: 2400,
	})
	if err != nil {
		t.Fatalf("scrobble start: %v", err)
	}
	if gotPath != "/scrobble/start" {
		t.Fatalf("got path %q, want /scrobble/start", gotPath)
	}
	if progress, _ := gotBody["progress"].(float64); progress != 25 {
		t.Fatalf("expected progress 25, got %v", gotBody["progress"])
	}
	if season, _ := gotBody["season"].(float64); int(season) != 3 {
		t.Fatalf("expected season 3, got %v", gotBody["season"])
	}
	show, _ := gotBody["show"].(map[string]any)
	ids, _ := show["ids"].(map[string]any)
	if ids["imdb"] != "tt0903747" {
		t.Fatalf("expected show imdb tt0903747, got %v", ids["imdb"])
	}
}

func TestExportWatchlistSendsToWatchlistAdd(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"added":1,"existing":0,"not_found":0}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	favorites := []watchsync.LocalFavorite{
		{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0111161"},
		{MediaItemID: "s1", Kind: historyimport.KindSeries, IMDbID: "tt0903747"},
	}
	result, err := p.ExportWatchlist(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, favorites)
	if err != nil {
		t.Fatalf("export watchlist: %v", err)
	}
	if !strings.HasPrefix(gotPath, "/watchlist/items/add") {
		t.Fatalf("got path %q, want /watchlist/items/add", gotPath)
	}
	if len(result.Sent) == 0 {
		t.Fatal("expected sent results")
	}
	if _, ok := gotBody["movies"].([]any); !ok {
		t.Fatalf("expected movies array in body: %#v", gotBody)
	}
	if _, ok := gotBody["shows"].([]any); !ok {
		t.Fatalf("expected shows array in body: %#v", gotBody)
	}
}
