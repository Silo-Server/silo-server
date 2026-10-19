package sections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

type fixedClock time.Time

func (f fixedClock) Now() time.Time { return time.Time(f) }

type stubSeasonalList []seasonalListEntry

func (s stubSeasonalList) Get(_ context.Context, listURL string) (SeasonalListSnapshot, bool, error) {
	return SeasonalListSnapshot{ListURL: listURL, Entries: s}, true, nil
}

type stubThemeNeighbors []string

func (s stubThemeNeighbors) ThemeNeighbors(context.Context, []string, int, catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	out := make([]recommendations.ScoredItem, 0, len(s))
	for _, id := range s {
		out = append(out, recommendations.ScoredItem{MediaItemID: id})
	}
	return out, nil
}

// TestSeasonalRowShowsOwnedListTitles drives a seasonal row against the
// catalog: the in-season list's owned titles in list order, within the
// viewer's rating limit, topped up with theme neighbors unless the row is
// lists only, titled by the vibe, and empty off season.
func TestSeasonalRowShowsOwnedListTitles(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	id := func(name string) string { return fmt.Sprintf("seasonal-%s-%d", name, suffix) }
	tmdb := func(n int) string { return fmt.Sprintf("%d%d", n, suffix%1_000_000_000) }
	ids := []string{id("popular-r"), id("pg-movie"), id("pg-show"), id("neighbor")}
	var libraryID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		id("library")).Scan(&libraryID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, tmdb_id, tvdb_id, content_rating, content_rating_age) VALUES
			($1, 'movie',  'Popular R Movie', $5, '', 'R', 17),
			($2, 'movie',  'PG Movie',        $6, '', 'PG', 8),
			($3, 'series', 'PG Show',         '', $7, 'TV-PG', 8),
			($4, 'movie',  'Neighbor',        '', '', 'PG', 8)`,
		ids[0], ids[1], ids[2], ids[3], tmdb(1), tmdb(2), tmdb(3)); err != nil {
		t.Fatalf("seed media items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id) SELECT unnest($1::text[]), $2`,
		ids, libraryID); err != nil {
		t.Fatalf("seed media item libraries: %v", err)
	}

	list := stubSeasonalList{
		{TMDB: tmdb(1), Type: "movie"},
		{TMDB: tmdb(9), Type: "movie"}, // not in the library
		{TMDB: tmdb(2), Type: "movie"},
		{TVDB: tmdb(3), Type: "show"},
	}
	october := fixedClock(time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC))
	newFetcher := func(clock recipes.Clock) *Fetcher {
		f := NewFetcher(pool)
		f.Clock = clock
		f.SeasonalLists = list
		f.SeasonalNeighbors = stubThemeNeighbors{ids[1], ids[3]}
		return f
	}
	section := func(params recipes.SeasonalThemedParams, limit int) ResolvedSection {
		config, _ := json.Marshal(params)
		return ResolvedSection{ID: "seasonal-test", SectionType: SectionSeasonalThemed, Title: "Seasonal Picks", ItemLimit: limit, Config: config}
	}
	pgViewer := catalog.AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	contentIDs := func(t *testing.T, s ResolvedSection, f *Fetcher, filter catalog.AccessFilter) []string {
		t.Helper()
		items, _, err := f.fetchSeasonalThemed(ctx, s, &libraryID, nil, filter)
		if err != nil {
			t.Fatalf("fetchSeasonalThemed: %v", err)
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, item.ContentID)
		}
		return out
	}
	halloween := recipes.SeasonalThemedParams{EnabledThemes: []string{"halloween"}}

	t.Run("lists only keeps list order within the rating limit", func(t *testing.T) {
		got := contentIDs(t, section(recipes.SeasonalThemedParams{EnabledThemes: []string{"halloween"}, Source: recipes.SeasonalSourceLists}, 10), newFetcher(october), pgViewer)
		assertIDs(t, got, ids[1], ids[2])
	})
	t.Run("recommendations fill a short row without repeating list titles", func(t *testing.T) {
		got := contentIDs(t, section(halloween, 10), newFetcher(october), pgViewer)
		assertIDs(t, got, ids[1], ids[2], ids[3])
	})
	t.Run("a full row is cut at the limit", func(t *testing.T) {
		got := contentIDs(t, section(halloween, 2), newFetcher(october), catalog.AccessFilter{})
		assertIDs(t, got, ids[0], ids[1])
	})
	t.Run("off season is empty", func(t *testing.T) {
		got := contentIDs(t, section(halloween, 10), newFetcher(fixedClock(time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC))), pgViewer)
		assertIDs(t, got)
	})
	t.Run("the row takes the vibe's title", func(t *testing.T) {
		params := recipes.SeasonalThemedParams{EnabledThemes: []string{"halloween"}, Vibes: map[string]string{"halloween": "horror"}, Source: recipes.SeasonalSourceLists}
		result, err := newFetcher(october).FetchOne(ctx, section(params, 10), &libraryID, nil, 0, "", catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("FetchOne: %v", err)
		}
		theme, _ := recipes.SeasonalThemeByKey("halloween")
		if want := theme.Vibe("horror").Title; result.Title != want {
			t.Errorf("title = %q, want %q", result.Title, want)
		}
	})
}

func assertIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("content IDs = %v, want %v", got, want)
	}
}

type memorySeasonalLists struct {
	snapshots map[string]SeasonalListSnapshot
	failures  map[string]string
}

func (m *memorySeasonalLists) Get(_ context.Context, listURL string) (SeasonalListSnapshot, bool, error) {
	s, ok := m.snapshots[listURL]
	return s, ok, nil
}

func (m *memorySeasonalLists) TryClaimRefresh(context.Context, string, time.Duration) (time.Time, bool, error) {
	return time.Unix(1, 0), true, nil
}

func (m *memorySeasonalLists) SaveSuccess(_ context.Context, listURL string, entries []seasonalListEntry, status string, _, refreshedAt time.Time) error {
	m.snapshots[listURL] = SeasonalListSnapshot{ListURL: listURL, Entries: entries, RefreshedAt: &refreshedAt, LastStatus: status}
	return nil
}

func (m *memorySeasonalLists) RecordFailure(_ context.Context, listURL, message string, _ time.Time) error {
	m.failures[listURL] = message
	return nil
}

type seasonalConfigs []json.RawMessage

func (c seasonalConfigs) ListSeasonalConfigs(context.Context) ([]json.RawMessage, error) {
	return c, nil
}

// TestSeasonalListRefresherFetchesUpcomingStaleLists covers what one
// scheduled run downloads: lists a row shows now or within the lookahead,
// once each, skipping fresh copies, and keeping the last good copy when a
// download fails or comes back empty.
func TestSeasonalListRefresherFetchesUpcomingStaleLists(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) // Thanksgiving starts Nov 22
	thanksgiving, _ := recipes.SeasonalThemeByKey("thanksgiving")
	halloween, _ := recipes.SeasonalThemeByKey("halloween")
	christmas, _ := recipes.SeasonalThemeByKey("christmas")
	canonical := func(raw string) string {
		key, err := collectionutil.CanonicalMDBListURL(raw)
		if err != nil {
			t.Fatalf("canonical %q: %v", raw, err)
		}
		return key
	}
	thanksgivingURL := canonical(thanksgiving.Vibes[0].ListURL)
	christmasURL := canonical(christmas.Vibe("family").ListURL)
	freshAt := now.Add(-time.Hour)
	store := &memorySeasonalLists{
		snapshots: map[string]SeasonalListSnapshot{
			christmasURL: {ListURL: christmasURL, Entries: []seasonalListEntry{{TMDB: "1", Type: "movie"}}, RefreshedAt: &freshAt},
		},
		failures: map[string]string{},
	}
	config := json.RawMessage(`{"enabled_themes":["halloween","thanksgiving","christmas"],"vibes":{"christmas":"family"}}`)
	var fetched []string
	r := NewSeasonalListRefresher(seasonalConfigs{config, config}, store)
	r.Clock = fixedClock(now)
	r.Fetch = func(_ context.Context, listURL string) ([]seasonalListEntry, error) {
		fetched = append(fetched, listURL)
		return []seasonalListEntry{{TMDB: "42", Type: "movie"}}, nil
	}

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	// Thanksgiving is two days out and stale; Christmas (Dec 1) is within the
	// lookahead but fresh; Halloween is past.
	assertIDs(t, fetched, thanksgivingURL)
	if _, ok := store.snapshots[canonical(halloween.Vibes[0].ListURL)]; ok {
		t.Error("refreshed an out-of-season list")
	}

	// A failed download keeps the stored entries.
	r.Clock = fixedClock(now.Add(seasonalListMaxAge + time.Hour))
	r.Fetch = func(context.Context, string) ([]seasonalListEntry, error) { return nil, errors.New("mdblist down") }
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := store.snapshots[thanksgivingURL].Entries; len(got) != 1 || got[0].TMDB != "42" {
		t.Errorf("failed refresh replaced the stored list: %+v", got)
	}
	if store.failures[thanksgivingURL] == "" {
		t.Error("failed refresh was not recorded")
	}

	// So does an empty answer.
	delete(store.failures, thanksgivingURL)
	r.Fetch = func(context.Context, string) ([]seasonalListEntry, error) { return []seasonalListEntry{}, nil }
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := store.snapshots[thanksgivingURL].Entries; len(got) != 1 {
		t.Errorf("empty refresh replaced the stored list: %+v", got)
	}
	if store.failures[thanksgivingURL] == "" {
		t.Error("empty refresh was not recorded")
	}
}

// TestSeasonalEntriesFromMDBList pins how an MDBList /json feed maps to list
// entries: TMDB, IMDb and TVDB IDs kept, null or zero IDs dropped, and rows
// that are neither movies nor shows skipped.
// recordingThemeNeighbors records the access filter the fill searches with.
type recordingThemeNeighbors struct{ filter catalog.AccessFilter }

func (r *recordingThemeNeighbors) ThemeNeighbors(_ context.Context, _ []string, _ int, filter catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	r.filter = filter
	return nil, nil
}

// TestSeasonalFillSearchesOnlyTheRowsLibraries pins that a library page's
// fill looks for neighbors in that library alone, within the viewer's access.
func TestSeasonalFillSearchesOnlyTheRowsLibraries(t *testing.T) {
	neighbors := &recordingThemeNeighbors{}
	f := &Fetcher{SeasonalNeighbors: neighbors}
	library := 7
	f.seasonalNeighborItems(context.Background(), []string{"movie:a"}, 5, 5, &library, nil, catalog.AccessFilter{AllowedLibraryIDs: []int{3, 7}})
	if !slices.Equal(neighbors.filter.AllowedLibraryIDs, []int{7}) {
		t.Fatalf("fill searched libraries %v, want [7]", neighbors.filter.AllowedLibraryIDs)
	}
}

// recordingResolver answers external-ID lookups from fixed maps and records
// the batches it was asked for.
type recordingResolver struct {
	batches map[string]catalog.ExternalIDBatch
	lookups map[string]*catalog.ExternalIDLookup
}

func (r *recordingResolver) GetByExternalIDs(_ context.Context, batch catalog.ExternalIDBatch, itemType string) (*catalog.ExternalIDLookup, error) {
	r.batches[itemType] = batch
	if lookup := r.lookups[itemType]; lookup != nil {
		return lookup, nil
	}
	return &catalog.ExternalIDLookup{ByTMDB: map[string]string{}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}}, nil
}

// TestResolveSeasonalEntriesDedupesAndFallsBackToTVDB covers a list naming a
// title twice and a movie the library knows only by its TVDB ID.
func TestResolveSeasonalEntriesDedupesAndFallsBackToTVDB(t *testing.T) {
	resolver := &recordingResolver{
		batches: map[string]catalog.ExternalIDBatch{},
		lookups: map[string]*catalog.ExternalIDLookup{
			"movie": {ByTMDB: map[string]string{"1": "movie:one"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{"77": "movie:tvdb"}},
		},
	}
	entries := []seasonalListEntry{
		{TMDB: "1", Type: listEntryMovie},
		{TMDB: "1", IMDb: "tt1", Type: listEntryMovie},
		{TMDB: "2", TVDB: "77", Type: listEntryMovie},
	}
	ids, err := resolveSeasonalEntries(context.Background(), resolver, entries)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"movie:one", "movie:tvdb"}; !slices.Equal(ids, want) {
		t.Fatalf("resolved %v, want %v", ids, want)
	}
	if got := resolver.batches["movie"].TMDBIDs; !slices.Equal(got, []string{"1", "2"}) {
		t.Fatalf("movie TMDB batch %v, want each ID once", got)
	}
}

func TestSeasonalEntriesFromMDBList(t *testing.T) {
	var items []mdblistListItem
	feed := `[
		{"id":10439,"rank":1,"title":"Hocus Pocus","imdb_id":"tt0107120","tvdbid":null,"mediatype":"movie","release_year":1993},
		{"id":0,"rank":2,"title":"Wednesday","imdb_id":"tt13443470","tvdbid":397060,"mediatype":"show","release_year":2022},
		{"id":5,"rank":3,"title":"Someone","imdb_id":"nm1","tvdbid":null,"mediatype":"person"},
		{"id":0,"rank":4,"title":"Unknown","imdb_id":"","tvdbid":0,"mediatype":"movie"}
	]`
	if err := json.Unmarshal([]byte(feed), &items); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	got := seasonalEntriesFromMDBList(items)
	want := []seasonalListEntry{
		{TMDB: "10439", IMDb: "tt0107120", Type: "movie"},
		{IMDb: "tt13443470", TVDB: "397060", Type: "show"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("entries = %+v, want %+v", got, want)
	}
}

// TestSeasonalListRepositoryKeepsLastGoodList covers the snapshot table: a
// claim blocks a second worker, a save replaces the entries, and a failed
// download keeps them.
func TestSeasonalListRepositoryKeepsLastGoodList(t *testing.T) {
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
	listURL, err := collectionutil.CanonicalMDBListURL(fmt.Sprintf("https://mdblist.com/lists/silo-test/seasonal-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM seasonal_list_snapshots WHERE list_url = $1`, listURL)
	})
	repo := NewSeasonalListRepository(pool)
	entries := []seasonalListEntry{{TMDB: "10439", IMDb: "tt0107120", Type: "movie"}, {TVDB: "397060", Type: "show"}}

	claimAt, ok, err := repo.TryClaimRefresh(ctx, listURL, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if _, ok, err := repo.TryClaimRefresh(ctx, listURL, time.Minute); err != nil || ok {
		t.Fatalf("second claim during the lease = %v, %v; want refused", ok, err)
	}
	if err := repo.SaveSuccess(ctx, listURL, entries, "ok", claimAt, time.Now()); err != nil {
		t.Fatalf("SaveSuccess: %v", err)
	}

	claimAt, ok, err = repo.TryClaimRefresh(ctx, listURL, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim after save = %v, %v", ok, err)
	}
	if err := repo.RecordFailure(ctx, listURL, "mdblist down", claimAt); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	got, found, err := repo.Get(ctx, listURL)
	if err != nil || !found {
		t.Fatalf("Get = %v, %v", found, err)
	}
	if fmt.Sprint(got.Entries) != fmt.Sprint(entries) || got.LastStatus != "error" || got.LastError != "mdblist down" {
		t.Fatalf("after failure: entries %v status %q error %q; want last good entries kept", got.Entries, got.LastStatus, got.LastError)
	}
}
