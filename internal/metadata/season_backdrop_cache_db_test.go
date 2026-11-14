package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestSeasonBackdropBackfillAndRepairDB: a season backdrop from a provider is
// queued by the provider-artwork backfill and by targeted repair, like a
// season poster, and its job publishes to the backdrop columns. Reconcile
// resets a missing provider copy to its source and relies on these to cache it
// again.
func TestSeasonBackdropBackfillAndRepairDB(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	ctx := context.Background()
	repo := NewImageCacheJobRepository(pool)

	prefix := fmt.Sprintf("zz-season-backdrop-%d-", time.Now().UnixNano())
	series, uncached, missing := prefix+"series", prefix+"s1", prefix+"s2"
	cleanup := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM metadata_image_cache_jobs WHERE target_content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
	}
	cleanup()
	t.Cleanup(cleanup)
	// Repair callers pass the original key of a missing revision.
	missingKey := "tvdb/series/" + series + "/seasons/2/backdrop/original.r.webp"
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, tvdb_id) VALUES ($1, 'series', $1, '403245')`, series); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO seasons (content_id, series_id, season_number, title, overview, backdrop_source_path, backdrop_path) VALUES
			($1, $3, 1, 'Season 1', '', 'tvdb://banners/season-1-backdrop.jpg', ''),
			($2, $3, 2, 'Season 2', '', 'tvdb://banners/season-2-backdrop.jpg', $4)`, uncached, missing, series, missingKey); err != nil {
		t.Fatalf("seed seasons: %v", err)
	}
	queued := func(t *testing.T) map[string]*models.MetadataImageCacheJob {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT target_content_id, image_type, source_path, season_number FROM metadata_image_cache_jobs
			WHERE target_type = 'season' AND target_content_id LIKE $1`, prefix+"%")
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		defer rows.Close()
		jobs := map[string]*models.MetadataImageCacheJob{}
		for rows.Next() {
			job := &models.MetadataImageCacheJob{TargetType: ImageCacheTargetSeason}
			if err := rows.Scan(&job.TargetContentID, &job.ImageType, &job.SourcePath, &job.SeasonNumber); err != nil {
				t.Fatalf("scan job: %v", err)
			}
			jobs[job.TargetContentID] = job
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		return jobs
	}

	for surface := 0; surface < imageCacheDiscoverySurfaceCount; surface++ {
		if _, err := repo.EnqueueExistingProviderArtwork(ctx, imageCacheDiscoveryCursor{Surface: surface, Key: prefix}, 10); err != nil {
			t.Fatalf("backfill surface %d: %v", surface, err)
		}
	}
	got := queued(t)
	job := got[uncached]
	if len(got) != 1 || job == nil || job.ImageType != ImageCacheImageBackdrop || job.SourcePath != "tvdb://banners/season-1-backdrop.jpg" || job.SeasonNumber == nil || *job.SeasonNumber != 1 {
		t.Fatalf("backfill queued %v, want the uncached season 1 backdrop only", got)
	}
	if current, err := repo.CurrentTargetSourcePath(ctx, job); err != nil || current != job.SourcePath {
		t.Fatalf("current source = %q, %v, want the backdrop source", current, err)
	}
	seasons := catalog.NewSeasonRepository(pool)
	if ok, err := seasons.UpdateArtworkIfSourceMatches(ctx, uncached, ImageCacheImageBackdrop, job.SourcePath, "tvdb/cached/backdrop.webp", "th"); err != nil || !ok {
		t.Fatalf("publish backdrop = %v, %v", ok, err)
	}
	var backdrop, thumbhash, poster string
	err := pool.QueryRow(ctx, `SELECT backdrop_path, backdrop_thumbhash, COALESCE(poster_path, '') FROM seasons WHERE content_id = $1`, uncached).Scan(&backdrop, &thumbhash, &poster)
	if err != nil || backdrop != "tvdb/cached/backdrop.webp" || thumbhash != "th" || poster != "" {
		t.Fatalf("season after publish = %q/%q, poster %q, %v; want the backdrop columns only", backdrop, thumbhash, poster, err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("clear jobs: %v", err)
	}
	n, err := repo.EnqueueArtworkRepair(ctx, []string{missingKey}, 10)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if got := queued(t); n != 1 || len(got) != 1 || got[missing] == nil || got[missing].ImageType != ImageCacheImageBackdrop || got[missing].SourcePath != "tvdb://banners/season-2-backdrop.jpg" {
		t.Fatalf("repair queued %d jobs %v, want the season 2 backdrop from its provider source", n, got)
	}
}
