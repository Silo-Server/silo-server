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
	fetch := func(config string) []string {
		t.Helper()
		items, total, err := NewFetcher(pool).fetchRecentlyAdded(ctx, ResolvedSection{
			SectionType: SectionRecentlyAdded,
			ItemLimit:   20,
			Config:      json.RawMessage(config),
		}, nil, libraries, catalog.AccessFilter{})
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

	video := fetch(string(defaultMediaScopeConfig(catalog.MediaScopeVideo)))
	if want := []string{"episode:" + episodeID, "movie:" + movieID}; !slices.Equal(video, want) {
		t.Fatalf("video row = %v, want %v", video, want)
	}
	all := fetch(`{}`)
	if want := []string{"audiobook:" + bookID, "episode:" + episodeID, "movie:" + movieID}; !slices.Equal(all, want) {
		t.Fatalf("unscoped row = %v, want %v", all, want)
	}
}

func TestGeneratedHomeRowsSkipVideoLibrariesWithSharedRowDB(t *testing.T) {
	f := newSectionMutationFixture(t)
	ctx := t.Context()

	home, err := f.repo.ListByScopeAll(ctx, "home", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(home, IsSharedHomeRecentlyAddedSection) {
		// Older homes have no shared row and keep per-library rows.
		created, err := f.repo.CreateGeneratedHomeLibraryRecentSections(ctx, f.library, "Legacy Movies", "movies")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.repo.DeleteGeneratedHomeLibraryRecentSections(context.Background(), f.library) })
		if len(created) == 0 {
			t.Fatal("home without a shared row created no per-library rows")
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
}
