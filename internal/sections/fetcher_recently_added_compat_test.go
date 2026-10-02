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

func TestFetchTVRecentlyAddedCompatibilityOptOut(t *testing.T) {
	fetcher := &Fetcher{}
	items, total, handled, err := fetcher.fetchTVRecentlyAdded(
		context.Background(),
		ResolvedSection{SectionType: SectionRecentlyAdded, DisableTVEventGrouping: true},
		nil,
		nil,
		catalog.AccessFilter{},
	)
	if err != nil {
		t.Fatalf("fetchTVRecentlyAdded: %v", err)
	}
	if handled || total != 0 || items != nil {
		t.Fatalf("opt-out returned items=%v total=%d handled=%v; want generic fallback", items, total, handled)
	}
}

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
