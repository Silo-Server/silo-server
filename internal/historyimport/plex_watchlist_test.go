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
	fixture := newHistoryImportFixture(t)
	pool := fixture.pool
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
		fixture.id("movie-693134"), KindMovie, "Dune: Part Two", 2024, "693134", "matched",
	); err != nil {
		t.Fatalf("seed media item: %v", err)
	}
	run, err := repo.CreateRun(ctx, Run{
		ID:               fixture.id("plex-watchlist-duplicate-run"),
		UserID:           fixture.userID,
		ProfileID:        fixture.profileID,
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

	completed, err := repo.GetRunForUser(ctx, fixture.userID, run.ID)
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
		fixture.userID, fixture.profileID, fixture.id("movie-693134"),
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

// newHistoryImportFixture connects to SILO_TEST_DATABASE_URL (skipping when
// unset) and seeds the account an import test runs as, removing it on cleanup.
func newHistoryImportFixture(t *testing.T) historyImportFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	// history_import_runs is keyed on (user_id, profile_id) against
	// user_profiles, so the import path needs a real account behind it.
	fixture := historyImportFixture{pool: pool, suffix: fmt.Sprintf("%d", time.Now().UnixNano())}
	fixture.profileID = fixture.id("profile")

	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ($1, $2, 'x', 'user')
		RETURNING id`,
		fixture.id("history-import-test"),
		fixture.id("history-import-test")+"@example.test",
	).Scan(&fixture.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		// user_profiles, user_watchlist, user_favorites and
		// history_import_runs all cascade from the user.
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM users WHERE id = $1`, fixture.userID); err != nil {
			t.Errorf("clean up user: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx,
			`DELETE FROM media_items WHERE content_id LIKE '%-' || $1`, fixture.suffix); err != nil {
			t.Errorf("clean up media items: %v", err)
		}
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, $3)`,
		fixture.profileID, fixture.userID, "History Import Test",
	); err != nil {
		t.Fatalf("seed user profile: %v", err)
	}
	return fixture
}

// historyImportFixture holds the account an import test runs as. Identifiers
// derived from it carry a nanosecond suffix so binaries sharing one database
// stay off each other's rows, and so the fixture can clean up by suffix.
type historyImportFixture struct {
	pool      *pgxpool.Pool
	suffix    string
	userID    int
	profileID string
}

// id returns name scoped to this fixture's run, so identifiers are unique
// across binaries sharing one database.
func (f historyImportFixture) id(name string) string { return name + "-" + f.suffix }
