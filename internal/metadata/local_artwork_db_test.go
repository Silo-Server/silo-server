package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func localArtworkTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestCurrentTargetCachedPathItem(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("local-art-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
		VALUES ($1, 'movie', 'Local Art Test', 'matched', '{}'::text[], $2, $3)
	`, contentID, "local/movies/"+contentID+"/deadbeef/poster/original.webp", "file:///media/movies/Film/poster.jpg"); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})

	repo := NewImageCacheJobRepository(pool)
	job := &models.MetadataImageCacheJob{
		TargetType:      ImageCacheTargetItem,
		TargetContentID: contentID,
		ImageType:       ImageCacheImagePoster,
	}
	cached, err := repo.CurrentTargetCachedPath(ctx, job)
	if err != nil {
		t.Fatalf("CurrentTargetCachedPath: %v", err)
	}
	if want := "local/movies/" + contentID + "/deadbeef/poster/original.webp"; cached != want {
		t.Fatalf("cached = %q, want %q", cached, want)
	}
	source, err := repo.CurrentTargetSourcePath(ctx, job)
	if err != nil {
		t.Fatalf("CurrentTargetSourcePath: %v", err)
	}
	if source != "file:///media/movies/Film/poster.jpg" {
		t.Fatalf("source = %q", source)
	}

	// Missing rows report empty, not an error.
	missing, err := repo.CurrentTargetCachedPath(ctx, &models.MetadataImageCacheJob{
		TargetType:      ImageCacheTargetItem,
		TargetContentID: contentID + "-missing",
		ImageType:       ImageCacheImagePoster,
	})
	if err != nil || missing != "" {
		t.Fatalf("missing row: cached=%q err=%v", missing, err)
	}
}

func TestEnqueueBatchAcceptsLocalSourceDB(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	contentID := fmt.Sprintf("local-art-enq-%d", time.Now().UnixNano())
	repo := NewImageCacheJobRepository(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = $1`, contentID)
	})

	n, err := repo.EnqueueBatch(ctx, []EnqueueImageCacheJobInput{{
		TargetType:      ImageCacheTargetItem,
		TargetContentID: contentID,
		SeriesID:        contentID,
		SourcePath:      "file:///media/movies/Film/poster.jpg",
		ContentType:     "movies",
		ImageType:       ImageCacheImagePoster,
	}})
	if err != nil {
		t.Fatalf("EnqueueBatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("enqueued %d, want 1", n)
	}
	var providerID string
	if err := pool.QueryRow(ctx,
		`SELECT provider_id FROM metadata_image_cache_jobs WHERE target_content_id = $1`, contentID,
	).Scan(&providerID); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if providerID != "local" {
		t.Fatalf("provider_id = %q, want local", providerID)
	}
}

// TestEnqueueRequeuesSucceededJobOnlyWhenAskedDB pins the per-input requeue: a
// succeeded job for an unchanged source stays succeeded unless its input asks
// to run again, and one batch can carry both kinds of input.
func TestEnqueueRequeuesSucceededJobOnlyWhenAskedDB(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	repo := NewImageCacheJobRepository(pool)
	prefix := fmt.Sprintf("local-art-requeue-%d", time.Now().UnixNano())
	kept, rerun := prefix+"-kept", prefix+"-rerun"
	ids := []string{kept, rerun}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = ANY($1)`, ids)
	})
	input := func(contentID string, requeue bool) EnqueueImageCacheJobInput {
		return EnqueueImageCacheJobInput{
			TargetType:       ImageCacheTargetItem,
			TargetContentID:  contentID,
			SeriesID:         contentID,
			SourcePath:       "file:///media/Other/Show/poster.jpg",
			ContentType:      "series",
			ImageType:        ImageCacheImagePoster,
			RequeueSucceeded: requeue,
		}
	}

	if _, err := repo.EnqueueBatch(ctx, []EnqueueImageCacheJobInput{input(kept, false), input(rerun, false)}); err != nil {
		t.Fatalf("enqueue jobs: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs
		SET status = 'succeeded', attempt_count = 1, completed_at = NOW()
		WHERE target_content_id = ANY($1)
	`, ids); err != nil {
		t.Fatalf("mark jobs succeeded: %v", err)
	}

	n, err := repo.EnqueueBatch(ctx, []EnqueueImageCacheJobInput{input(kept, false), input(rerun, true)})
	if err != nil {
		t.Fatalf("re-enqueue jobs: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-enqueue affected %d rows, want 1", n)
	}
	if status, attempts := readImageCacheJobState(t, pool, kept); status != ImageCacheStatusSucceeded || attempts != 1 {
		t.Fatalf("unflagged job = %s/%d, want %s/1", status, attempts, ImageCacheStatusSucceeded)
	}
	if status, attempts := readImageCacheJobState(t, pool, rerun); status != ImageCacheStatusQueued || attempts != 0 {
		t.Fatalf("flagged job = %s/%d, want %s/0", status, attempts, ImageCacheStatusQueued)
	}
}
