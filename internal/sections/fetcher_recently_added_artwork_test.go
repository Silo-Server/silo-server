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

func TestFetchOneRecentTVEpisodePreservesStillArtworkOwnerAfterCache(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	resetResolvedListCacheForTest()
	t.Cleanup(resetResolvedListCacheForTest)

	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("recent-artwork-series-%d", suffix)
	episodeID := fmt.Sprintf("recent-artwork-episode-%d", suffix)
	runID := fmt.Sprintf("recent-artwork-run-%d", suffix)
	seriesBackdrop := fmt.Sprintf("provider://recent-artwork/%d/series-backdrop.jpg", suffix)
	episodeStill := fmt.Sprintf("provider://recent-artwork/%d/episode-still.jpg", suffix)

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', $1, true)
		RETURNING id
	`, seriesID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_files WHERE episode_id = $1`, episodeID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM scan_runs WHERE id = $1`, runID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items
			(content_id, type, title, status, genres, backdrop_path, created_at)
		VALUES ($1, 'series', 'Recent Artwork Show', 'matched', '{}'::text[], $2, $3)
	`, seriesID, seriesBackdrop, base); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, $3)
	`, seriesID, folderID, base); err != nil {
		t.Fatalf("seed series membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO scan_runs (id, media_folder_id, mode, status)
		VALUES ($1, $2, 'library', 'completed')
	`, runID, folderID); err != nil {
		t.Fatalf("seed scan run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes
			(content_id, series_id, season_number, episode_number, title, still_path)
		VALUES ($1, $2, 1, 1, 'Pilot', $3)
	`, episodeID, seriesID, episodeStill); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episode_libraries
			(episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id)
		VALUES ($1, $2, $3, $4)
	`, episodeID, folderID, base.Add(time.Minute), runID); err != nil {
		t.Fatalf("seed episode membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (episode_id, media_folder_id, file_path)
		VALUES ($1, $2, $3)
	`, episodeID, folderID, "/recent-artwork/"+episodeID+".mkv"); err != nil {
		t.Fatalf("seed episode file: %v", err)
	}

	fetcher := NewFetcher(pool)
	resolved := ResolvedSection{
		ID:          fmt.Sprintf("recent-artwork-section-%d", suffix),
		SectionType: SectionRecentlyAdded,
		ItemLimit:   20,
		Config:      json.RawMessage(`{"filter_type":"series"}`),
	}
	assertFetch := func(stage string) {
		t.Helper()
		got, err := fetcher.FetchOne(ctx, resolved, &folderID, nil, 0, "", catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("%s FetchOne: %v", stage, err)
		}
		if got.TotalCount != 1 || len(got.Items) != 1 {
			t.Fatalf("%s result has total=%d items=%d, want one item", stage, got.TotalCount, len(got.Items))
		}

		item := got.Items[0]
		if item == nil || item.Type != "episode" || item.ContentID != episodeID {
			t.Fatalf("%s item = %#v, want episode %q", stage, item, episodeID)
		}
		if item.PlayContentID != episodeID {
			t.Fatalf("%s play content ID = %q, want %q", stage, item.PlayContentID, episodeID)
		}
		if item.BackdropPath != episodeStill {
			t.Fatalf("%s backdrop path = %q, want episode still %q", stage, item.BackdropPath, episodeStill)
		}

		meta, ok := got.ItemMeta[episodeID]
		if !ok {
			t.Fatalf("%s metadata missing for episode %q", stage, episodeID)
		}
		wantOwner := SectionArtworkOwner{Kind: SectionArtworkOwnerEpisode, ContentID: episodeID}
		if meta.BackdropOwner != wantOwner {
			t.Fatalf("%s backdrop owner = %+v, want %+v", stage, meta.BackdropOwner, wantOwner)
		}
	}

	assertFetch("cold load")
	assertFetch("cache hit")
}
