package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestItemOnlyArtworkBackfillAndRepairDB: a banner, thumb, clear art or disc
// image from a provider is queued by the manual provider-artwork backfill and
// by targeted repair, like a poster. Reconcile resets a missing provider copy
// to its source and relies on these two to cache it again. Sidecar sources
// stay with the refresh.
func TestItemOnlyArtworkBackfillAndRepairDB(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	ctx := context.Background()
	repo := NewImageCacheJobRepository(pool)

	prefix := fmt.Sprintf("zz-item-only-backfill-%d-", time.Now().UnixNano())
	item := prefix + "movie"
	cleanup := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = $1`, item)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, item)
	}
	cleanup()
	t.Cleanup(cleanup)
	// Repair callers pass the original key of a missing revision.
	const missingClearArt = "local/movies/item/aa/clearart/original.r.webp"
	const missingSidecarThumb = "local/movies/item/bb/thumb/original.r.webp"
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, tmdb_id,
			banner_source_path, banner_path,
			thumb_source_path, thumb_path,
			clearart_source_path, clearart_path,
			disc_source_path, disc_path)
		VALUES ($1, 'movie', $1, '10378',
			'https://image.example.test/banner.jpg', '',
			'file:///media/Movie/landscape.jpg', $3,
			'https://image.example.test/clearart.png', $2,
			'https://image.example.test/disc.png', 'https://image.example.test/disc.png')`, item, missingClearArt, missingSidecarThumb); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	queued := func(t *testing.T) map[string]string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT image_type, source_path FROM metadata_image_cache_jobs WHERE target_type = 'item' AND target_content_id = $1`, item)
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		defer rows.Close()
		jobs := map[string]string{}
		for rows.Next() {
			var imageType, source string
			if err := rows.Scan(&imageType, &source); err != nil {
				t.Fatalf("scan job: %v", err)
			}
			jobs[imageType] = source
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		return jobs
	}

	for surface := 10; surface < imageCacheDiscoverySurfaceCount; surface++ {
		if _, err := repo.EnqueueExistingProviderArtwork(ctx, imageCacheDiscoveryCursor{Surface: surface, Key: prefix}, 10); err != nil {
			t.Fatalf("backfill surface %d: %v", surface, err)
		}
	}
	got := queued(t)
	want := map[string]string{
		ImageCacheImageBanner: "https://image.example.test/banner.jpg",
		ImageCacheImageDisc:   "https://image.example.test/disc.png",
	}
	if len(got) != len(want) || got[ImageCacheImageBanner] != want[ImageCacheImageBanner] || got[ImageCacheImageDisc] != want[ImageCacheImageDisc] {
		t.Fatalf("backfill queued %v, want the uncached provider banner and disc only", got)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = $1`, item); err != nil {
		t.Fatalf("clear jobs: %v", err)
	}
	// The sidecar thumb is missing too, but its recovery is the refresh.
	n, err := repo.EnqueueArtworkRepair(ctx, []string{missingClearArt, missingSidecarThumb}, 10)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if got := queued(t); n != 1 || len(got) != 1 || got[ImageCacheImageClearArt] != "https://image.example.test/clearart.png" {
		t.Fatalf("repair queued %d jobs %v, want the clear art from its provider source", n, got)
	}
}
