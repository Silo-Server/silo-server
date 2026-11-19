package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCompatibilityRecentlyAddedReturnsFlatDistinctSeries(t *testing.T) {
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
	seriesID := fmt.Sprintf("compat-latest-series-%d", suffix)
	episodes := []string{
		fmt.Sprintf("compat-latest-episode-a-%d", suffix),
		fmt.Sprintf("compat-latest-episode-b-%d", suffix),
	}
	runs := []string{
		fmt.Sprintf("compat-latest-run-a-%d", suffix),
		fmt.Sprintf("compat-latest-run-b-%d", suffix),
	}
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, seriesID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	base := time.Date(2026, 8, 7, 16, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, created_at) VALUES ($1, 'series', 'Compat Show', 'matched', '{}'::text[], $2)`, seriesID, base); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, seriesID, folderID, base); err != nil {
		t.Fatalf("seed series membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO scan_runs (id, media_folder_id, mode, status) VALUES ($1, $3, 'library', 'completed'), ($2, $3, 'library', 'completed')`, runs[0], runs[1], folderID); err != nil {
		t.Fatalf("seed runs: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $3, 1, 1, 'One'), ($2, $3, 1, 2, 'Two')`, episodes[0], episodes[1], seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id) VALUES ($1, $3, $4, $5), ($2, $3, $6, $7)`, episodes[0], episodes[1], folderID, base.Add(time.Minute), runs[0], base.Add(2*time.Minute), runs[1]); err != nil {
		t.Fatalf("seed episode memberships: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_files (episode_id, media_folder_id, file_path) VALUES ($1, $3, $4), ($2, $3, $5)`, episodes[0], episodes[1], folderID, "/compat/"+episodes[0]+".mkv", "/compat/"+episodes[1]+".mkv"); err != nil {
		t.Fatalf("seed files: %v", err)
	}

	items, total, err := NewFetcher(pool).fetchRecentlyAdded(ctx, ResolvedSection{
		SectionType:            SectionRecentlyAdded,
		ItemLimit:              20,
		Config:                 json.RawMessage(`{"filter_type":"series"}`),
		DisableTVEventGrouping: true,
	}, &folderID, nil, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("fetch compat recently added: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Type != "series" || items[0].ContentID != seriesID {
		t.Fatalf("items=%#v total=%d; want one distinct series", items, total)
	}
}

// Recently Released orders a row the way the library's Release Date sort
// does: by the release date (a series' premiere when it has no release date),
// newest first, whatever order the titles were added in. Titles with no date
// come last. Ties, undated titles included, go by content ID, as on the See All
// page, so the row stays that page's first page. A movie-only row, like its See
// All page, treats a movie with only a premiere date as undated.
func TestRecentlyReleasedOrdersByReleaseDate(t *testing.T) {
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
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, fmt.Sprintf("recently-released-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID) })

	// Seeded newest release first, so the newest-added title is the oldest
	// release.
	seeds := []struct {
		name, kind, releaseDate, firstAirDate string
		year                                  int
	}{
		{name: "sep-30", kind: "movie", releaseDate: "2026-09-30", year: 2026},
		{name: "tie-a", kind: "movie", releaseDate: "2026-08-01", year: 2026},
		{name: "tie-b", kind: "movie", releaseDate: "2026-08-01", year: 2026},
		{name: "jun-17", kind: "movie", releaseDate: "2026-06-17", year: 2026},
		{name: "movie-premiere-only", kind: "movie", firstAirDate: "2026-07-01", year: 2026},
		{name: "undated-b", kind: "movie", year: 2026},
		{name: "series-may-02", kind: "series", firstAirDate: "2026-05-02", year: 2026},
		{name: "dec-2025", kind: "movie", releaseDate: "2025-12-01", year: 2025},
		{name: "undated-a", kind: "movie", year: 2024},
		{name: "mar-12", kind: "movie", releaseDate: "2026-03-12", year: 2026},
		{name: "jan-20", kind: "movie", releaseDate: "2026-01-20", year: 2026},
	}
	ids := make(map[string]string, len(seeds))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i, seed := range seeds {
		id := fmt.Sprintf("recently-released-%s-%d", seed.name, suffix)
		ids[id] = seed.name
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, id) })
		added := base.Add(time.Duration(i) * time.Hour)
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, year, release_date, first_air_date, created_at)
			VALUES ($1, $2, $3, 'matched', '{}'::text[], $4, NULLIF($5, '')::date, NULLIF($6, ''), $7)`,
			id, seed.kind, seed.name, seed.year, seed.releaseDate, seed.firstAirDate, added); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, folderID, added); err != nil {
			t.Fatalf("seed %s membership: %v", seed.name, err)
		}
	}

	order := func(config string) []string {
		t.Helper()
		items, _, err := NewFetcher(pool).fetchRecentlyReleased(ctx, ResolvedSection{
			SectionType: SectionRecentlyReleased,
			ItemLimit:   20,
			Config:      json.RawMessage(config),
		}, &folderID, nil, catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("fetch recently released (%s): %v", config, err)
		}
		got := make([]string, 0, len(items))
		for _, item := range items {
			got = append(got, ids[item.ContentID])
		}
		return got
	}
	for _, tc := range []struct {
		config string
		want   []string
	}{
		{`{}`, []string{"sep-30", "tie-a", "tie-b", "movie-premiere-only", "jun-17", "series-may-02", "mar-12", "jan-20", "dec-2025", "undated-a", "undated-b"}},
		{`{"filter_type":"movie"}`, []string{"sep-30", "tie-a", "tie-b", "jun-17", "mar-12", "jan-20", "dec-2025", "movie-premiere-only", "undated-a", "undated-b"}},
	} {
		if got := order(tc.config); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("%s order = %v\nwant    %v", tc.config, got, tc.want)
		}
	}
}
