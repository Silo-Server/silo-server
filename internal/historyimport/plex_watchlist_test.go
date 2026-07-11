package historyimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
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
	items, warnings, err := client.FetchWatchlist(context.Background(), "account-token-1")
	if err != nil {
		t.Fatalf("FetchWatchlist: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none (all items carried guids)", warnings)
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

// The discover listing does not honor includeGuids in practice: items arrive
// without external ids, and some detail responses key their payload on
// "Video" instead of "Metadata". Both must be handled or matching silently
// degrades to exact title/year.
func TestFetchWatchlistResolvesGuidsViaItemMetadata(t *testing.T) {
	detailCalls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/sections/watchlist/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":2,"Metadata":[
				{"ratingKey":"wl-movie","type":"movie","title":"Dune: Part Two","year":2024},
				{"ratingKey":"wl-show","type":"show","title":"Severance","year":2022}
			]}}`)
		case "/library/metadata/wl-movie":
			detailCalls["wl-movie"]++
			// Movie detail keyed on "Video" (discover inconsistency).
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Video":[
				{"ratingKey":"wl-movie","type":"movie","title":"Dune: Part Two","year":2024,
				 "Guid":[{"id":"imdb://tt15239678"},{"id":"tmdb://693134"}]}
			]}}`)
		case "/library/metadata/wl-show":
			detailCalls["wl-show"]++
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"wl-show","type":"show","title":"Severance","year":2022,
				 "Guid":[{"id":"tvdb://371980"}]}
			]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewPlexClient()
	client.discoverBaseURL = server.URL
	items, warnings, err := client.FetchWatchlist(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchWatchlist: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none (all ids resolved)", warnings)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if detailCalls["wl-movie"] != 1 || detailCalls["wl-show"] != 1 {
		t.Fatalf("detail fetches = %v, want one per id-less item", detailCalls)
	}
	movie := NormalizePlexWatchlistItem(items[0])
	if movie.IMDbID != "tt15239678" || movie.TMDBID != "693134" {
		t.Fatalf("movie ids = imdb %q tmdb %q, want resolved from detail fetch", movie.IMDbID, movie.TMDBID)
	}
	show := NormalizePlexWatchlistItem(items[1])
	if show.TVDBID != "371980" {
		t.Fatalf("show tvdb id = %q, want resolved from detail fetch", show.TVDBID)
	}
}

// A failed per-item metadata fetch must not sink the watchlist: the item
// falls back to title/year matching and the fetch reports one warning.
func TestFetchWatchlistWarnsWhenGuidResolutionFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/sections/watchlist/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"wl-1","type":"movie","title":"Dune: Part Two","year":2024}
			]}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client := NewPlexClient()
	client.discoverBaseURL = server.URL
	items, warnings, err := client.FetchWatchlist(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchWatchlist: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Dune: Part Two" {
		t.Fatalf("items = %+v, want the listing entry kept", items)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one unresolved-ids warning", warnings)
	}
}

// Guard against page-size regressions: a server reporting a huge total but
// returning empty pages must not loop forever.
func TestFetchWatchlistStopsOnEmptyPage(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":999,"Metadata":[]}}`)
	}))
	defer server.Close()

	client := NewPlexClient()
	client.discoverBaseURL = server.URL
	items, _, err := client.FetchWatchlist(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchWatchlist: %v", err)
	}
	if len(items) != 0 || calls != 1 {
		t.Fatalf("items=%d calls=%d, want 0 items after a single call", len(items), calls)
	}
}

func TestPlexWatchlistImportCountsOnlyInsertedRows(t *testing.T) {
	ctx := context.Background()
	pool := newPlexWatchlistImportTestPool(t)
	repo := NewRepository(pool, nil)
	service := &Service{
		repo:         repo,
		matcher:      NewMatcher(repo),
		stores:       pgstore.NewPostgresProvider(pool),
		bgContext:    ctx,
		runSemaphore: make(chan struct{}, maxConcurrentRuns),
		runCancels:   make(map[string]context.CancelFunc),
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, year, tmdb_id, status)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		"movie-693134", KindMovie, "Dune: Part Two", 2024, "693134", "matched",
	); err != nil {
		t.Fatalf("seed media item: %v", err)
	}
	run, err := repo.CreateRun(ctx, Run{
		ID:               "plex-watchlist-duplicate-run",
		UserID:           42,
		ProfileID:        "profile-1",
		SourceType:       SourceTypePlex,
		ConnectionMode:   ConnectionModePlexOAuth,
		Status:           RunStatusQueued,
		Warnings:         []string{},
		UnmatchedSamples: []UnmatchedSample{},
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	updatedAt := time.Date(2026, time.July, 2, 12, 0, 0, 0, time.UTC)
	record := Record{
		Kind:        KindMovie,
		Title:       "Dune: Part Two",
		Year:        2024,
		TMDBID:      "693134",
		Watchlisted: true,
		UpdatedAt:   updatedAt,
	}
	service.executeRun(run, staticWatchlistProvider{records: []Record{record, record}})

	completed, err := repo.GetRunForUser(ctx, 42, run.ID)
	if err != nil {
		t.Fatalf("GetRunForUser: %v", err)
	}
	if completed.Status != RunStatusCompleted {
		t.Fatalf("run status = %q, want %q; warnings=%v", completed.Status, RunStatusCompleted, completed.Warnings)
	}
	if completed.WatchlistAdded != 1 {
		t.Fatalf("WatchlistAdded = %d, want 1", completed.WatchlistAdded)
	}
	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM user_watchlist
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		42, "profile-1", "movie-693134",
	).Scan(&rows); err != nil {
		t.Fatalf("count watchlist rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("watchlist rows = %d, want 1", rows)
	}
}

type staticWatchlistProvider struct {
	records []Record
}

func (p staticWatchlistProvider) Fetch(context.Context) ([]Record, []string, error) {
	return p.records, nil, nil
}

func newPlexWatchlistImportTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse db config: %v", err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, stmt := range []string{
		`CREATE TEMP TABLE media_items (
			content_id text PRIMARY KEY,
			type text NOT NULL,
			title text NOT NULL,
			year integer,
			imdb_id text,
			tmdb_id text,
			tvdb_id text,
			status text NOT NULL DEFAULT 'matched'
		) ON COMMIT PRESERVE ROWS`,
		`CREATE TEMP TABLE user_watchlist (
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			media_item_id text NOT NULL,
			added_at timestamptz NOT NULL DEFAULT now(),
			sort_index integer,
			PRIMARY KEY (user_id, profile_id, media_item_id)
		) ON COMMIT PRESERVE ROWS`,
		`CREATE TEMP TABLE history_import_runs (
			id text PRIMARY KEY,
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			source_type text NOT NULL,
			connection_mode text NOT NULL,
			status text NOT NULL,
			mapping_id integer,
			fetched integer NOT NULL DEFAULT 0,
			matched integer NOT NULL DEFAULT 0,
			unmatched integer NOT NULL DEFAULT 0,
			progress_updated integer NOT NULL DEFAULT 0,
			history_created integer NOT NULL DEFAULT 0,
			watchlist_added integer NOT NULL DEFAULT 0,
			favorites_imported integer NOT NULL DEFAULT 0,
			skipped integer NOT NULL DEFAULT 0,
			warnings jsonb NOT NULL DEFAULT '[]'::jsonb,
			unmatched_samples jsonb NOT NULL DEFAULT '[]'::jsonb,
			error_message text,
			created_at timestamptz NOT NULL DEFAULT now(),
			started_at timestamptz,
			completed_at timestamptz,
			last_heartbeat_at timestamptz
		) ON COMMIT PRESERVE ROWS`,
		`CREATE TEMP TABLE user_favorites (
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			media_item_id text NOT NULL,
			added_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, profile_id, media_item_id)
		) ON COMMIT PRESERVE ROWS`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("create temp test table: %v", err)
		}
	}
	return pool
}
