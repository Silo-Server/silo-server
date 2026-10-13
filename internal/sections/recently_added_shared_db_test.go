package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func openRecentlyAddedTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSharedRecentlyAddedMergesTVEventsWithMoviesDB(t *testing.T) {
	pool := openRecentlyAddedTestPool(t)
	ctx := t.Context()
	suffix := time.Now().UnixNano()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	folder := func(kind string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`, kind, fmt.Sprintf("shared-recent-%s-%d", kind, suffix)).Scan(&id); err != nil {
			t.Fatalf("seed %s folder: %v", kind, err)
		}
		return id
	}
	moviesID, showsID, booksID := folder("movies"), folder("series"), folder("audiobooks")

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	movieID := fmt.Sprintf("shared-recent-movie-%d", suffix)
	seriesID := fmt.Sprintf("shared-recent-series-%d", suffix)
	episodeID := fmt.Sprintf("shared-recent-episode-%d", suffix)
	bookID := fmt.Sprintf("shared-recent-book-%d", suffix)
	runID := fmt.Sprintf("shared-recent-run-%d", suffix)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieID, seriesID, bookID})
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{moviesID, showsID, booksID})
	})

	// The show itself is old; its new episode is what should surface, above the
	// movie added a minute earlier. The audiobook is newest of all.
	exec(`INSERT INTO media_items (content_id, type, title, status, genres, created_at) VALUES
		($1, 'movie', 'Shared Movie', 'matched', '{}'::text[], $4),
		($2, 'series', 'Shared Show', 'matched', '{}'::text[], $5),
		($3, 'audiobook', 'Shared Book', 'matched', '{}'::text[], $6)`,
		movieID, seriesID, bookID, base.Add(time.Minute), base.Add(-30*24*time.Hour), base.Add(3*time.Minute))
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3), ($4, $5, $6), ($7, $8, $9)`,
		movieID, moviesID, base.Add(time.Minute),
		seriesID, showsID, base.Add(-30*24*time.Hour),
		bookID, booksID, base.Add(3*time.Minute))
	exec(`INSERT INTO scan_runs (id, media_folder_id, mode, status) VALUES ($1, $2, 'library', 'completed')`, runID, showsID)
	exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, rating_imdb) VALUES ($1, $2, 2, 1, 'Premiere', '', 45, 0)`, episodeID, seriesID)
	exec(`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id) VALUES ($1, $2, $3, $4)`, episodeID, showsID, base.Add(2*time.Minute), runID)
	exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path) VALUES ($1, $2, $3)`, episodeID, showsID, "/shared-recent/"+episodeID+".mkv")

	libraries := []int{moviesID, showsID, booksID}
	fetchIn := func(config string, scope []int) []string {
		t.Helper()
		items, total, err := NewFetcher(pool).fetchRecentlyAdded(ctx, ResolvedSection{
			SectionType: SectionRecentlyAdded,
			ItemLimit:   20,
			Config:      json.RawMessage(config),
		}, nil, scope, catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("fetch recently added %s: %v", config, err)
		}
		if total < len(items) {
			t.Fatalf("total %d below page size %d", total, len(items))
		}
		ids := make([]string, len(items))
		for i, item := range items {
			ids[i] = item.Type + ":" + item.ContentID
		}
		return ids
	}
	fetch := func(config string) []string {
		t.Helper()
		return fetchIn(config, libraries)
	}

	video := fetch(string(defaultMediaScopeConfig(catalog.MediaScopeVideo)))
	if want := []string{"episode:" + episodeID, "movie:" + movieID}; !slices.Equal(video, want) {
		t.Fatalf("video row = %v, want %v", video, want)
	}
	all := fetch(`{}`)
	if want := []string{"audiobook:" + bookID, "episode:" + episodeID, "movie:" + movieID}; !slices.Equal(all, want) {
		t.Fatalf("unscoped row = %v, want %v", all, want)
	}

	// A row configured for both video libraries, read in a caller scope of
	// only the movie library, must not show the TV library's cards.
	configured := fmt.Sprintf(`{"filter_library_ids":[%d,%d]}`, moviesID, showsID)
	if got, want := fetchIn(configured, []int{moviesID}), []string{"movie:" + movieID}; !slices.Equal(got, want) {
		t.Fatalf("configured row in movie scope = %v, want %v", got, want)
	}

	// As in the plain query, the configured libraries and the caller's scope
	// are separate membership checks: a movie that is also in the configured
	// books library stays on a row configured for shows and books when the
	// caller's scope is shows and movies.
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, movieID, booksID, base.Add(time.Minute))
	configured = fmt.Sprintf(`{"filter_library_ids":[%d,%d]}`, showsID, booksID)
	if got, want := fetchIn(configured, []int{showsID, moviesID}), []string{"episode:" + episodeID, "movie:" + movieID}; !slices.Equal(got, want) {
		t.Fatalf("row configured for shows and books in shows+movies scope = %v, want %v", got, want)
	}
}

func TestGeneratedHomeRowsSkipVideoLibrariesWithSharedRowDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	ctx := t.Context()

	home, err := f.repo.ListByScopeAll(ctx, "home", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEnabledHomeRecentlyAddedCovering(home, "movies") {
		// Older homes have no covering row, and a disabled covering row shows
		// nothing; both keep per-library rows.
		disabled, err := f.repo.Create(ctx, &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Title: f.app + "-disabled", ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig(catalog.MediaScopeVideo), Enabled: false})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id = $1`, disabled.ID)
		})
		created, err := f.repo.CreateGeneratedHomeLibraryRecentSections(ctx, f.library, "Legacy Movies", "movies")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.repo.DeleteGeneratedHomeLibraryRecentSections(context.Background(), f.library) })
		if len(created) == 0 {
			t.Fatal("home without an enabled shared row created no per-library rows")
		}
		if err := f.repo.DeleteGeneratedHomeLibraryRecentSections(ctx, f.library); err != nil {
			t.Fatal(err)
		}

		shared, err := f.repo.Create(ctx, &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Title: f.app, ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig(catalog.MediaScopeVideo), Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id = $1`, shared.ID) })
	}

	created, err := f.repo.CreateGeneratedHomeLibraryRecentSections(ctx, f.library, "New Movies", "movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 0 {
		t.Fatalf("video library created %d home rows beside the shared row", len(created))
	}

	books, err := f.repo.CreateGeneratedHomeLibraryRecentSections(ctx, f.library, "New Books", "ebooks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.repo.DeleteGeneratedHomeLibraryRecentSections(context.Background(), f.library) })
	if len(books) == 0 {
		t.Fatal("book library created no home rows")
	}
}

func TestHasEnabledHomeRecentlyAddedCovering(t *testing.T) {
	row := func(scope string, enabled bool) *PageSection {
		return &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: defaultMediaScopeConfig(scope), Enabled: enabled}
	}
	shared := func(enabled bool) *PageSection { return row(catalog.MediaScopeVideo, enabled) }
	movieOnly := row("movie", true)
	seriesOnly := row("series", true)
	tests := []struct {
		name        string
		home        []*PageSection
		libraryType string
		want        bool
	}{
		{"enabled shared row covers movies", []*PageSection{movieOnly, shared(true)}, "movies", true},
		{"enabled shared row covers mixed", []*PageSection{shared(true)}, "mixed", true},
		{"disabled shared row covers nothing", []*PageSection{shared(false)}, "series", false},
		{"movie row covers movies", []*PageSection{movieOnly}, "movies", true},
		{"movie row misses series", []*PageSection{movieOnly}, "series", false},
		{"split rows cover mixed", []*PageSection{movieOnly, seriesOnly}, "mixed", true},
		{"movie row alone misses mixed", []*PageSection{movieOnly}, "mixed", false},
		{"disabled split row misses mixed", []*PageSection{movieOnly, row("series", false)}, "mixed", false},
		{"no rows", nil, "movies", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasEnabledHomeRecentlyAddedCovering(tt.home, tt.libraryType); got != tt.want {
				t.Fatalf("hasEnabledHomeRecentlyAddedCovering(%q) = %v, want %v", tt.libraryType, got, tt.want)
			}
		})
	}
}

func TestMergeRecentlyAddedInterleavesAndReportsLeftover(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tvCard := func(id string, minutes int) *models.MediaItem {
		added := base.Add(time.Duration(minutes) * time.Minute)
		return &models.MediaItem{ContentID: id, AddedAt: &added}
	}
	otherCard := func(id string, minutes int) *models.MediaItem {
		return &models.MediaItem{ContentID: id, CreatedAt: base.Add(time.Duration(minutes) * time.Minute)}
	}
	ids := func(items []*models.MediaItem) []string {
		out := make([]string, len(items))
		for i, item := range items {
			out[i] = item.ContentID
		}
		return out
	}
	tv := []*models.MediaItem{tvCard("show-a", 5), tvCard("shared", 3), tvCard("show-b", 1)}
	other := []*models.MediaItem{otherCard("movie-a", 4), otherCard("shared", 2)}

	merged, leftover := mergeRecentlyAdded(tv, other, 10)
	if want := []string{"show-a", "movie-a", "shared", "show-b"}; !slices.Equal(ids(merged), want) {
		t.Fatalf("merged = %v, want %v", ids(merged), want)
	}
	if leftover {
		t.Fatal("a merge that consumed both lists reported leftover cards")
	}

	merged, leftover = mergeRecentlyAdded(tv, other, 2)
	if want := []string{"show-a", "movie-a"}; !slices.Equal(ids(merged), want) {
		t.Fatalf("limited merge = %v, want %v", ids(merged), want)
	}
	if !leftover {
		t.Fatal("a merge cut by the limit reported no leftover cards")
	}

	// A series in both a TV library and a mixed library fills both sides with
	// the same cards. Each side read one past a page of 2; with only those
	// cards, nothing exists beyond the page.
	tv = []*models.MediaItem{tvCard("show-a", 5), tvCard("show-b", 3)}
	other = []*models.MediaItem{otherCard("show-b", 4), otherCard("show-a", 2)}
	merged, leftover = mergeRecentlyAdded(tv, other, 2)
	if want := []string{"show-a", "show-b"}; !slices.Equal(ids(merged), want) {
		t.Fatalf("duplicate merge = %v, want %v", ids(merged), want)
	}
	if leftover {
		t.Fatal("cards left only as duplicates of merged cards reported leftover")
	}
}

func TestScopeTVLibraries(t *testing.T) {
	tests := []struct {
		name  string
		tv    []int
		scope []int
		want  []int
	}{
		{"every library", []int{3, 5}, nil, []int{3, 5}},
		{"narrowed by caller", []int{3, 5, 7}, []int{5, 9}, []int{5}},
		{"disjoint", []int{3}, []int{5}, []int{}},
		{"caller sees nothing", []int{3}, []int{}, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scopeTVLibraries(tt.tv, tt.scope); !slices.Equal(got, tt.want) {
				t.Fatalf("scopeTVLibraries = %v, want %v", got, tt.want)
			}
		})
	}
}

// The default home Recently Added TV row is scoped to series. A TV library
// typed with an alias such as "tv" counts as covered by that row, so the row
// has to show it.
func TestSeriesRecentlyAddedIncludesTVAliasLibrariesDB(t *testing.T) {
	pool := openRecentlyAddedTestPool(t)
	ctx := t.Context()
	suffix := time.Now().UnixNano()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var showsID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('tv', $1, true) RETURNING id`, fmt.Sprintf("alias-recent-tv-%d", suffix)).Scan(&showsID); err != nil {
		t.Fatalf("seed tv folder: %v", err)
	}
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seriesID := fmt.Sprintf("alias-recent-series-%d", suffix)
	episodeID := fmt.Sprintf("alias-recent-episode-%d", suffix)
	runID := fmt.Sprintf("alias-recent-run-%d", suffix)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = $1`, showsID)
	})
	exec(`INSERT INTO media_items (content_id, type, title, status, genres, created_at) VALUES ($1, 'series', 'Alias Show', 'matched', '{}'::text[], $2)`, seriesID, base)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, seriesID, showsID, base)
	exec(`INSERT INTO scan_runs (id, media_folder_id, mode, status) VALUES ($1, $2, 'library', 'completed')`, runID, showsID)
	exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, rating_imdb) VALUES ($1, $2, 1, 1, 'Pilot', '', 45, 0)`, episodeID, seriesID)
	exec(`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id) VALUES ($1, $2, $3, $4)`, episodeID, showsID, base.Add(time.Minute), runID)
	exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path) VALUES ($1, $2, $3)`, episodeID, showsID, "/alias-recent/"+episodeID+".mkv")

	items, _, err := NewFetcher(pool).fetchRecentlyAdded(ctx, ResolvedSection{
		SectionType: SectionRecentlyAdded,
		ItemLimit:   20,
		Config:      defaultMediaScopeConfig("series"),
	}, nil, []int{showsID}, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("fetch series row: %v", err)
	}
	got := make([]string, len(items))
	for i, item := range items {
		got[i] = item.Type + ":" + item.ContentID
	}
	if want := []string{"episode:" + episodeID}; !slices.Equal(got, want) {
		t.Fatalf("series row over a tv-typed library = %v, want %v", got, want)
	}
}
