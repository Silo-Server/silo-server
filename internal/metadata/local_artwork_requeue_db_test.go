package metadata_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/nfo"
)

// TestReplacedLocalSidecarRequeuesSucceededJobDB pins re-caching a sidecar
// replaced under the same name: discovery's size+mtime fingerprint changes
// the source path, so the succeeded job re-queues even though the slot's
// cached copy means the enqueue does not ask for a requeue.
func TestReplacedLocalSidecarRequeuesSucceededJobDB(t *testing.T) {
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
	contentID := fmt.Sprintf("local-art-replace-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM metadata_image_cache_jobs WHERE target_content_id = $1`, contentID)
	})

	dir := t.TempDir()
	movie := filepath.Join(dir, "Film.mkv")
	poster := filepath.Join(dir, "poster.jpg")
	writeFixtureFile(t, movie, "video")
	writeFixtureFile(t, poster, "first poster")
	firstTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(poster, firstTime, firstTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	provider := nfo.NewProvider()
	discover := func() string {
		t.Helper()
		images, err := provider.GetImages(ctx, metadata.ImageRequest{
			ContentType:               "movie",
			RepresentativeFilePath:    movie,
			PrimarySidecarSearchPaths: []string{dir},
		})
		if err != nil || len(images) != 1 {
			t.Fatalf("GetImages = %+v, %v", images, err)
		}
		return images[0].URL
	}
	repo := metadata.NewImageCacheJobRepository(pool)
	enqueue := func(source string) {
		t.Helper()
		if _, err := repo.EnqueueBatch(ctx, []metadata.EnqueueImageCacheJobInput{{
			TargetType:      metadata.ImageCacheTargetItem,
			TargetContentID: contentID,
			SeriesID:        contentID,
			SourcePath:      source,
			ContentType:     "movies",
			ImageType:       metadata.ImageCacheImagePoster,
		}}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	jobState := func() (status, source string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT status, source_path FROM metadata_image_cache_jobs WHERE target_content_id = $1
		`, contentID).Scan(&status, &source); err != nil {
			t.Fatalf("read job: %v", err)
		}
		return status, source
	}

	first := discover()
	enqueue(first)
	if _, err := pool.Exec(ctx, `
		UPDATE metadata_image_cache_jobs SET status = 'succeeded', attempt_count = 1, completed_at = NOW()
		WHERE target_content_id = $1
	`, contentID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	enqueue(discover())
	if status, _ := jobState(); status != metadata.ImageCacheStatusSucceeded {
		t.Fatalf("unchanged sidecar: job status = %s, want it left succeeded", status)
	}

	writeFixtureFile(t, poster, "the replacement poster")
	replacedTime := firstTime.Add(time.Hour)
	if err := os.Chtimes(poster, replacedTime, replacedTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	replaced := discover()
	if replaced == first {
		t.Fatalf("replaced sidecar kept source %q", first)
	}
	enqueue(replaced)
	if status, source := jobState(); status != metadata.ImageCacheStatusQueued || source != replaced {
		t.Fatalf("replaced sidecar: job = %s %q, want queued for %q", status, source, replaced)
	}
}
