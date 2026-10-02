package mdblist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/logredact"
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
	if !caps.ImportRatings || !caps.ExportRatings {
		t.Fatalf("mdblist should sync ratings both ways: %#v", caps)
	}
	var _ watchsync.APIKeyAuthProvider = p
	var _ watchsync.RatingImporter = p
	var _ watchsync.RatingExporter = p
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

func TestConnectWithAPIKeyReportsRejectedKeyAsInvalidCredential(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		p := NewProvider(server.Client(), server.URL)
		_, _, err := p.ConnectWithAPIKey(context.Background(), "wrong-key")
		if !errors.Is(err, watchsync.ErrInvalidCredential) {
			t.Fatalf("status %d: err = %v, want watchsync.ErrInvalidCredential", status, err)
		}
		if !watchsync.IsInvalidCredentialError(err) {
			t.Fatalf("status %d: provider rejection not classified as invalid credential", status)
		}
		server.Close()
	}
}

func TestFetchWatchedImportsPlayableLeavesWithoutExpandingAggregateRows(t *testing.T) {
	watched := time.Date(2025, time.October, 21, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("plays"); got != "all" {
			t.Fatalf("plays query = %q, want all", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"movies": []map[string]any{{
				"last_watched_at": watched.Format(time.RFC3339),
				"movie": map[string]any{
					"title": "Beetlejuice Beetlejuice",
					"year":  2024,
					"ids":   map[string]any{"imdb": "tt2049403", "tmdb": 917496},
				},
			}},
			"shows": []map[string]any{{
				"last_watched_at": watched.Format(time.RFC3339),
				"show": map[string]any{
					"title": "Breaking Bad",
					"year":  2008,
					"ids":   map[string]any{"tvdb": 81189, "tmdb": 1396},
				},
			}},
			"seasons": []map[string]any{{
				"last_watched_at": watched.Format(time.RFC3339),
				"season": map[string]any{
					"number": 2,
					"name":   "Season 2",
					"ids":    map[string]any{"tmdb": 8160},
					"show": map[string]any{
						"title": "Breaking Bad",
						"year":  2008,
						"ids":   map[string]any{"imdb": "tt0903747", "tmdb": 1396},
					},
				},
			}},
			"episodes": []map[string]any{{
				"last_watched_at": watched.Format(time.RFC3339),
				"episode": map[string]any{
					"season": 1,
					"number": 2,
					"name":   "Cat's in the Bag...",
					"ids":    map[string]any{"tmdb": 62086},
					"show": map[string]any{
						"title": "Breaking Bad",
						"year":  2008,
						"ids":   map[string]any{"tvdb": 81189, "imdb": "tt0903747"},
					},
				},
			}},
			"pagination": map[string]any{"has_more": false},
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
	if rows[1].Title != "Cat's in the Bag..." || rows[1].SeriesIMDbID != "tt0903747" {
		t.Fatalf("expected nested episode/show fields propagated, got %#v", rows[1])
	}
	for i, row := range rows {
		if row.LastWatchedAt == nil || !row.LastWatchedAt.Equal(watched) {
			t.Fatalf("row %d watched timestamp = %v, want %v", i, row.LastWatchedAt, watched)
		}
	}
}

func TestFetchWatchedAcceptsLegacyWatchedAtAndFlatEpisode(t *testing.T) {
	watched := time.Date(2025, time.November, 1, 8, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"movies":[{"watched_at":"2025-11-01T08:30:00Z","movie":{"ids":{"imdb":"tt0111161"}}}],
			"episodes":[{"watched_at":"2025-11-01T08:30:00Z","season":1,"number":2,"show":{"ids":{"tvdb":81189}}}]
		}`))
	}))
	defer server.Close()

	rows, err := NewProvider(server.Client(), server.URL).FetchWatched(
		context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"},
	)
	if err != nil {
		t.Fatalf("fetch watched: %v", err)
	}
	if len(rows) != 2 || rows[0].LastWatchedAt == nil || !rows[0].LastWatchedAt.Equal(watched) ||
		rows[1].LastWatchedAt == nil || !rows[1].LastWatchedAt.Equal(watched) {
		t.Fatalf("unexpected legacy rows: %#v", rows)
	}
}

func TestFetchWatchedUsesCursorPagination(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "next-page" {
			_, _ = w.Write([]byte(`{"movies":[],"episodes":[],"pagination":{"next_cursor":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{"movies":[],"episodes":[],"pagination":{"next_cursor":"next-page"}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	if _, err := p.FetchWatched(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}); err != nil {
		t.Fatalf("fetch watched: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("got %d requests, want 2", len(queries))
	}
	if strings.Contains(queries[0], "offset=") || !strings.Contains(queries[1], "cursor=next-page") {
		t.Fatalf("unexpected pagination queries: %#v", queries)
	}
}

func TestFetchWatchedContinuesOffsetPaginationWhenHasMore(t *testing.T) {
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offsets = append(offsets, r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "1" {
			_, _ = w.Write([]byte(`{"movies":[],"episodes":[],"pagination":{"has_more":false}}`))
			return
		}
		_, _ = w.Write([]byte(`{"movies":[{"watched_at":"2025-11-01T08:30:00Z","movie":{"ids":{"imdb":"tt0111161"}}}],"episodes":[],"pagination":{"has_more":true}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	rows, err := p.FetchWatched(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch watched: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if len(offsets) != 2 || offsets[0] != "" || offsets[1] != "1" {
		t.Fatalf("unexpected offsets: %#v", offsets)
	}
}

func TestFetchWatchedOffsetCountsAggregateRows(t *testing.T) {
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offsets = append(offsets, r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "2" {
			_, _ = w.Write([]byte(`{"movies":[],"shows":[],"seasons":[],"episodes":[],"pagination":{"has_more":false}}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"shows":[{"last_watched_at":"2025-11-01T08:30:00Z","show":{"ids":{"tmdb":1396}}}],
			"seasons":[{"last_watched_at":"2025-11-01T08:30:00Z","season":{"number":1,"show":{"ids":{"tmdb":1396}}}}],
			"pagination":{"has_more":true}
		}`))
	}))
	defer server.Close()

	rows, err := NewProvider(server.Client(), server.URL).FetchWatched(
		context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"},
	)
	if err != nil {
		t.Fatalf("fetch watched: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want aggregate rows excluded", len(rows))
	}
	if len(offsets) != 2 || offsets[0] != "" || offsets[1] != "2" {
		t.Fatalf("unexpected offsets: %#v", offsets)
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

func TestExportHistoryDoesNotClaimNotFoundBatchWasSent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"added":{"movies":[]},"updated":{},"not_found":{"movies":[{"ids":{"imdb":"tt-missing"}}]}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	plays := []watchsync.LocalPlay{{HistoryID: "h1", Kind: historyimport.KindMovie, IMDbID: "tt-missing"}}
	result, err := p.ExportHistory(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, plays)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(result.Sent) != 0 || result.Failed["h1"] == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExportHistoryTreatsEmptyNotFoundBucketsAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"added":{"movies":1},"not_found":{"movies":[],"episodes":[]}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	plays := []watchsync.LocalPlay{{HistoryID: "h1", Kind: historyimport.KindMovie, IMDbID: "tt0111161"}}
	result, err := p.ExportHistory(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, plays)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(result.Sent) != 1 || len(result.Failed) != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExportHistoryReportsUnsupportedItemWithoutCallingMDBList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("MDBList should not be called for an unsupported item")
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	plays := []watchsync.LocalPlay{{HistoryID: "h1", Kind: historyimport.KindMovie}}
	result, err := p.ExportHistory(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, plays)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(result.Sent) != 0 || result.Failed["h1"] == "" {
		t.Fatalf("unexpected result: %#v", result)
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
	show, _ := gotBody["show"].(map[string]any)
	season, _ := show["season"].(map[string]any)
	if number, _ := season["number"].(float64); int(number) != 3 {
		t.Fatalf("expected show season number 3, got %v", season["number"])
	}
	episode, _ := season["episode"].(map[string]any)
	if number, _ := episode["number"].(float64); int(number) != 7 {
		t.Fatalf("expected show episode number 7, got %v", episode["number"])
	}
	if _, exists := gotBody["season"]; exists {
		t.Fatalf("season must be nested under show: %#v", gotBody)
	}
	if _, exists := show["episode"]; exists {
		t.Fatalf("episode must be nested under show season: %#v", show)
	}
	ids, _ := show["ids"].(map[string]any)
	if ids["imdb"] != "tt0903747" {
		t.Fatalf("expected show imdb tt0903747, got %v", ids["imdb"])
	}
}

func TestScrobbleRoundsProgressToMDBListPrecision(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	err := p.Start(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, watchsync.ScrobbleEvent{
		Kind:            historyimport.KindMovie,
		TMDBID:          "950387",
		PositionSeconds: 611.8,
		DurationSeconds: 2163.4,
	})
	if err != nil {
		t.Fatalf("scrobble start: %v", err)
	}
	if got := gotBody["progress"]; got != 28.28 {
		t.Fatalf("progress = %v, want 28.28", got)
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
		_, _ = w.Write([]byte(`{"added":{"movies":1,"shows":1},"existing":{"movies":0,"shows":0},"not_found":{"movies":0,"shows":0}}`))
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

func TestExportWatchlistDoesNotClaimNotFoundBatchWasSent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"added":{"movies":0,"shows":0},"existing":{"movies":0,"shows":0},"not_found":{"movies":1,"shows":0}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	favorites := []watchsync.LocalFavorite{{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0111161"}}
	result, err := p.ExportWatchlist(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, favorites)
	if err != nil {
		t.Fatalf("export watchlist: %v", err)
	}
	if len(result.Sent) != 0 || result.Failed["m1"] == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestRemoveWatchlistTreatsNotFoundBatchAsReconciled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/watchlist/items/remove" {
			t.Fatalf("path = %q, want /watchlist/items/remove", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"added":{"movies":0,"shows":0},"existing":{"movies":0,"shows":0},"not_found":{"movies":1,"shows":0}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	favorites := []watchsync.LocalFavorite{{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0111161"}}
	result, err := p.RemoveWatchlist(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, favorites)
	if err != nil {
		t.Fatalf("remove watchlist: %v", err)
	}
	if len(result.NotFound) != 1 || result.NotFound[0] != "m1" || len(result.Sent) != 0 || len(result.Failed) != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExportWatchlistReportsUnsupportedItemWithoutCallingMDBList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("MDBList should not be called for an unsupported item")
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	favorites := []watchsync.LocalFavorite{{MediaItemID: "m1", Kind: historyimport.KindMovie}}
	result, err := p.ExportWatchlist(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, favorites)
	if err != nil {
		t.Fatalf("export watchlist: %v", err)
	}
	if len(result.Sent) != 0 || result.Failed["m1"] == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestFetchWatchlistUsesCursorPagination(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "watch-next" {
			_, _ = w.Write([]byte(`{"movies":[],"shows":[],"pagination":{"next_cursor":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{"movies":[],"shows":[],"pagination":{"next_cursor":"watch-next"}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	if _, err := p.FetchWatchlist(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}); err != nil {
		t.Fatalf("fetch watchlist: %v", err)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "watch-next" {
		t.Fatalf("unexpected cursors: %#v", cursors)
	}
}

func TestFetchWatchlistContinuesOffsetPaginationWhenHasMore(t *testing.T) {
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offsets = append(offsets, r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "1" {
			_, _ = w.Write([]byte(`{"movies":[],"shows":[],"pagination":{"has_more":false}}`))
			return
		}
		_, _ = w.Write([]byte(`{"movies":[{"title":"The Shawshank Redemption","release_year":1994,"ids":{"imdb":"tt0111161"}}],"shows":[],"pagination":{"has_more":true}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	rows, err := p.FetchWatchlist(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch watchlist: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if len(offsets) != 2 || offsets[0] != "" || offsets[1] != "1" {
		t.Fatalf("unexpected offsets: %#v", offsets)
	}
}

func TestFetchWatchlistIgnoresPaginationTotal(t *testing.T) {
	// Only the ratings read checks total; the watchlist read still ends at
	// the first page without next_cursor or has_more.
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"movies":[{"title":"The Shawshank Redemption","release_year":1994,"ids":{"imdb":"tt0111161"}}],"shows":[],"pagination":{"total":5,"limit":1000,"next_cursor":null}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	rows, err := p.FetchWatchlist(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch watchlist: %v", err)
	}
	if requests != 1 || len(rows) != 1 {
		t.Fatalf("requests = %d rows = %d, want one page and one row", requests, len(rows))
	}
}

func TestDoRetriesShortRateLimitInPlace(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user_id": 7, "username": "retry"})
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	user, err := p.fetchUser(context.Background(), "key")
	if err != nil {
		t.Fatalf("fetch user after retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("got %d attempts, want 2", attempts)
	}
	if user.UserID != 7 {
		t.Fatalf("unexpected user %#v", user)
	}
}

func TestDoReturnsRateLimitedErrorWithoutRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	_, err := p.fetchUser(context.Background(), "key")
	rle, ok := watchsync.AsRateLimited(err)
	if !ok {
		t.Fatalf("expected RateLimitedError, got %v", err)
	}
	if rle.Provider != "mdblist" {
		t.Fatalf("got provider %q, want mdblist", rle.Provider)
	}
	// No Retry-After means burst limit and exhausted daily quota are
	// indistinguishable, so the provider defers a full hour.
	if rle.RetryAfter != defaultRetryAfter {
		t.Fatalf("got retry-after %s, want %s", rle.RetryAfter, defaultRetryAfter)
	}
	if attempts != 1 {
		t.Fatalf("got %d attempts, want 1 (no in-place retry for long waits)", attempts)
	}
}

func TestDoReturnsRateLimitedErrorWithLongRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	_, err := p.fetchUser(context.Background(), "key")
	rle, ok := watchsync.AsRateLimited(err)
	if !ok {
		t.Fatalf("expected RateLimitedError, got %v", err)
	}
	if rle.RetryAfter != time.Hour {
		t.Fatalf("got retry-after %s, want 1h", rle.RetryAfter)
	}
}

func TestDoReplaysBodyOnRateLimitRetry(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if len(bodies) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	err := p.do(context.Background(), http.MethodPost, "/watchlist/items/add", "key", strings.NewReader(`{"movies":[]}`), nil)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || bodies[0] != `{"movies":[]}` {
		t.Fatalf("body not replayed identically: %#v", bodies)
	}
}

func TestDoIncludesMDBListValidationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"progress":["Ensure that there are no more than 5 digits in total."]}}`))
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	err := p.do(context.Background(), http.MethodPost, "/scrobble/start", "key", strings.NewReader(`{}`), nil)
	if err == nil || !strings.Contains(err.Error(), "no more than 5 digits") {
		t.Fatalf("expected validation detail, got %v", err)
	}
}

func TestDoParsesHTTPDateRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(2*time.Hour).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	_, err := p.fetchUser(context.Background(), "key")
	rle, ok := watchsync.AsRateLimited(err)
	if !ok {
		t.Fatalf("expected RateLimitedError, got %v", err)
	}
	// HTTP-dates have one-second resolution, so allow for truncation.
	if rle.RetryAfter <= 2*time.Hour-5*time.Second || rle.RetryAfter > 2*time.Hour {
		t.Fatalf("got retry-after %s, want about 2h", rle.RetryAfter)
	}
}

func TestLimiterIsPerAPIKey(t *testing.T) {
	p := NewProvider(nil, "")
	first := p.limiter("a")
	second := p.limiter("a")
	if first != second {
		t.Fatal("same key should reuse one limiter")
	}
	if other := p.limiter("b"); other == first {
		t.Fatal("distinct keys should not share a limiter")
	}
}

func TestDoFloorsRetryAfterWhenRetriesExhausted(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	_, err := p.fetchUser(context.Background(), "key")
	rle, ok := watchsync.AsRateLimited(err)
	if !ok {
		t.Fatalf("expected RateLimitedError, got %v", err)
	}
	if attempts != maxRetryAttempts+1 {
		t.Fatalf("got %d attempts, want %d", attempts, maxRetryAttempts+1)
	}
	// The short Retry-After hints proved untrustworthy, so the deferral must
	// be floored rather than parroting the last 1s hint.
	if rle.RetryAfter != defaultRetryAfter {
		t.Fatalf("got retry-after %s, want floored %s", rle.RetryAfter, defaultRetryAfter)
	}
}

const sentinelAPIKey = "SENTINEL-KEY-123"

var errInjectedTransport = errors.New("injected transport failure")

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// assertErrorOmitsAPIKey fails when the key appears anywhere in err's chain,
// not only in its top-level message.
func assertErrorOmitsAPIKey(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), sentinelAPIKey) {
			t.Fatalf("error chain leaks the API key: %q", e.Error())
		}
	}
}

// keyedRequests covers each request shape the provider sends with the API key:
// GET with and without a query string, and POST with a JSON body.
var keyedRequests = []struct {
	name string
	call func(context.Context, *Provider) error
}{
	{"GET user", func(ctx context.Context, p *Provider) error {
		_, _, err := p.ConnectWithAPIKey(ctx, sentinelAPIKey)
		return err
	}},
	{"GET watched page", func(ctx context.Context, p *Provider) error {
		_, err := p.FetchWatched(ctx, watchsync.ServerConfig{}, sentinelConnection())
		return err
	}},
	{"GET playback", func(ctx context.Context, p *Provider) error {
		_, err := p.FetchProgress(ctx, watchsync.ServerConfig{}, sentinelConnection())
		return err
	}},
	{"POST watched", func(ctx context.Context, p *Provider) error {
		_, err := p.ExportHistory(ctx, watchsync.ServerConfig{}, sentinelConnection(), []watchsync.LocalPlay{
			{HistoryID: "h1", Kind: historyimport.KindMovie, IMDbID: "tt0111161"},
		})
		return err
	}},
	{"POST watchlist", func(ctx context.Context, p *Provider) error {
		_, err := p.ExportWatchlist(ctx, watchsync.ServerConfig{}, sentinelConnection(), []watchsync.LocalFavorite{
			{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0111161"},
		})
		return err
	}},
	{"POST scrobble", func(ctx context.Context, p *Provider) error {
		return p.Start(ctx, watchsync.ServerConfig{}, sentinelConnection(), watchsync.ScrobbleEvent{
			Kind:            historyimport.KindMovie,
			IMDbID:          "tt0111161",
			PositionSeconds: 60,
			DurationSeconds: 600,
		})
	}},
}

func sentinelConnection() watchsync.Connection {
	return watchsync.Connection{ID: "conn-1", AccessToken: sentinelAPIKey}
}

func TestRequestFailuresOmitAPIKey(t *testing.T) {
	sources := []struct {
		name        string
		newProvider func(t *testing.T) *Provider
		check       func(t *testing.T, err error)
	}{
		{
			name: "transport error",
			newProvider: func(*testing.T) *Provider {
				client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errInjectedTransport
				})}
				return NewProvider(client, "http://mdblist.test")
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errInjectedTransport) {
					t.Fatalf("lost the transport cause: %v", err)
				}
			},
		},
		{
			name: "connection refused",
			newProvider: func(*testing.T) *Provider {
				server := httptest.NewServer(http.NotFoundHandler())
				server.Close()
				return NewProvider(&http.Client{}, server.URL)
			},
			check: func(t *testing.T, err error) {
				var opErr *net.OpError
				if !errors.As(err, &opErr) {
					t.Fatalf("lost the dial error: %v", err)
				}
			},
		},
		{
			name: "malformed base URL",
			newProvider: func(*testing.T) *Provider {
				return NewProvider(&http.Client{}, "http://mdblist.test/\x7f")
			},
		},
		{
			name: "redirect loop",
			newProvider: func(t *testing.T) *Provider {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
				}))
				t.Cleanup(server.Close)
				return NewProvider(server.Client(), server.URL)
			},
		},
		{
			name: "unparseable redirect",
			newProvider: func(t *testing.T) *Provider {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", "http://%zz/?apikey="+r.URL.Query().Get("apikey"))
					w.WriteHeader(http.StatusFound)
				}))
				t.Cleanup(server.Close)
				return NewProvider(server.Client(), server.URL)
			},
		},
	}
	for _, source := range sources {
		for _, request := range keyedRequests {
			t.Run(source.name+"/"+request.name, func(t *testing.T) {
				err := request.call(context.Background(), source.newProvider(t))
				assertErrorOmitsAPIKey(t, err)
				if source.check != nil {
					source.check(t, err)
				}
			})
		}
	}
}

func TestRateLimitRetryFailureOmitsAPIKey(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": {"1"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		}
		return nil, errInjectedTransport
	})}
	p := NewProvider(client, "http://mdblist.test")

	err := p.do(context.Background(), http.MethodPost, "/watchlist/items/add", sentinelAPIKey, strings.NewReader(`{"movies":[]}`), nil)
	assertErrorOmitsAPIKey(t, err)
	if attempts != 2 {
		t.Fatalf("got %d attempts, want the in-place retry to run", attempts)
	}
	if !errors.Is(err, errInjectedTransport) {
		t.Fatalf("lost the transport cause: %v", err)
	}
}

func TestCanceledRequestStillMatchesContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		cancel()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	p := NewProvider(server.Client(), server.URL)
	_, _, err := p.ConnectWithAPIKey(ctx, sentinelAPIKey)
	assertErrorOmitsAPIKey(t, err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestTimedOutRequestStaysDetectable(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	cases := []struct {
		name   string
		client *http.Client
		ctx    func() (context.Context, context.CancelFunc)
	}{
		{
			name:   "client timeout",
			client: &http.Client{Timeout: 50 * time.Millisecond},
			ctx:    func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
		},
		{
			name:   "context deadline",
			client: &http.Client{},
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			p := NewProvider(tc.client, server.URL)
			_, _, err := p.ConnectWithAPIKey(ctx, sentinelAPIKey)
			assertErrorOmitsAPIKey(t, err)
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("expected a net.Error timeout, got %v", err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected context.DeadlineExceeded, got %v", err)
			}
		})
	}
}

func TestDoRedactsAPIKeyEchoedInErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unexpected failure for " + r.URL.RequestURI()})
	}))
	defer server.Close()

	p := NewProvider(server.Client(), server.URL)
	_, _, err := p.ConnectWithAPIKey(context.Background(), sentinelAPIKey)
	assertErrorOmitsAPIKey(t, err)
	if !strings.Contains(err.Error(), "unexpected failure for /user?apikey="+logredact.Placeholder) {
		t.Fatalf("expected the redacted body excerpt, got %v", err)
	}
}

func TestResponseErrorDetailDropsKeyFragmentCutByLimit(t *testing.T) {
	filler := strings.Repeat("x", maxErrorBodyBytes-10)
	detail := responseErrorDetail(strings.NewReader(filler+sentinelAPIKey+" tail"), sentinelAPIKey)
	if strings.Contains(detail, sentinelAPIKey[:10]) {
		t.Fatalf("excerpt keeps the key fragment cut by the limit: %q", detail[len(detail)-20:])
	}
	if detail != filler {
		t.Fatalf("got %d-byte excerpt, want the %d filler bytes", len(detail), len(filler))
	}
}

func TestRedactAPIKeyMasksRawAndEscapedForms(t *testing.T) {
	const key = "k/y+z 1"
	text := "raw=" + key + " escaped=" + url.QueryEscape(key)
	got := redactAPIKey(text, key)
	want := "raw=" + logredact.Placeholder + " escaped=" + logredact.Placeholder
	if got != want {
		t.Fatalf("redactAPIKey = %q, want %q", got, want)
	}
	if redactAPIKey("unchanged", "") != "unchanged" {
		t.Fatal("empty key must not alter text")
	}
}

func TestRequestErrorKeepsCauseClassificationWhenMaskingTheMessage(t *testing.T) {
	const key = "SENTINEL-KEY-123"
	cause := fmt.Errorf("GET https://api.mdblist.com/x?apikey=%s: %w", key, context.DeadlineExceeded)
	err := requestError("send", key, cause)
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), key) {
			t.Fatalf("chain exposes the key: %q", e.Error())
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to still match context.DeadlineExceeded", err)
	}
	var netErr net.Error
	timeout := &net.OpError{Op: "dial", Err: timeoutError{}}
	if !errors.As(requestError("send", key, fmt.Errorf("%s: %w", key, timeout)), &netErr) || !netErr.Timeout() {
		t.Fatal("a masked timeout must still be found as a net.Error")
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
