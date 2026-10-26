package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
