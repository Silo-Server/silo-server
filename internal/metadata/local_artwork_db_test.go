package metadata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/catalog"
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

// republishingItemTargets is the real item target. When republish is set, an
// admin picks the old copy again right after the image cache moves the item
// to its new one, publishing the key the re-cache just replaced.
type republishingItemTargets struct {
	items     *catalog.ItemRepository
	details   *catalog.DetailService
	republish string
}

func (r republishingItemTargets) UpdateArtworkIfSourceMatches(ctx context.Context, contentID, imageType, sourcePath, cachedPath, thumbhash string) (bool, error) {
	updated, err := r.items.UpdateArtworkIfSourceMatches(ctx, contentID, imageType, sourcePath, cachedPath, thumbhash)
	if err != nil || !updated || r.republish == "" {
		return updated, err
	}
	return true, r.details.PublishArtworkSelection(ctx, catalog.ArtworkSelection{
		TargetContentID: contentID, ImageType: imageType, StoredPath: r.republish,
		SourcePath: sourcePath, LockField: int(FieldImages),
	})
}

// A re-cached sidecar leaves its old copy to the artwork revision GC, which
// deletes it once nothing references it. An admin pick of the same bytes
// shares the old copy's key, so a copy published again in the meantime stays.
func TestRecachedSidecarRetiresTheOldCopyThroughRevisionGCDB(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	run := fmt.Sprintf("sidecar-recache-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, run+"-%")
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "local/movies/"+run+"-%")
	})
	items := catalog.NewItemRepository(pool)
	details := catalog.NewDetailService(items, catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool), catalog.NewPersonRepository(pool), nil)
	store := blobstoretest.New()
	root := t.TempDir()

	// recache stores oldPath as the item's cached sidecar, replaces the
	// sidecar's bytes, and runs its cache job.
	recache := func(name string, republish bool) (oldKeys []string) {
		t.Helper()
		contentID := run + "-" + name
		oldPath := "local/movies/" + contentID + "/00000000/poster/original.webp"
		sidecar := filepath.Join(root, name+".jpg")
		if err := os.WriteFile(sidecar, []byte("replaced "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		source := "file://" + sidecar
		if err := items.Upsert(ctx, &models.MediaItem{
			ContentID: contentID, Type: "movie", Title: name, Status: "matched",
			PosterPath: oldPath, PosterSourcePath: source,
		}); err != nil {
			t.Fatal(err)
		}
		oldKeys = artworkkey.ObjectKeys(oldPath, "poster")
		for _, key := range oldKeys {
			if err := store.Put(ctx, key, []byte("old")); err != nil {
				t.Fatal(err)
			}
		}
		job := localArtworkJob(source)
		job.TargetContentID, job.SeriesID, job.ProviderContentID = contentID, contentID, contentID
		jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{job}}
		targets := republishingItemTargets{items: items, details: details}
		if republish {
			targets.republish = oldPath
		}
		p := NewImageCacheProcessorWithTargets(jobs, &fakeByteImageCacher{}, nil, ImageCacheProcessorTargets{Items: targets})
		p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
		if stats, err := p.RunOnce(ctx, "w1", 10, 1); err != nil || stats.Succeeded != 1 {
			t.Fatalf("%s RunOnce = %+v, %v (failed text %q)", name, stats, err, jobs.failedText)
		}
		got, err := items.GetByID(ctx, contentID)
		if err != nil {
			t.Fatal(err)
		}
		if republish != (got.PosterPath == oldPath) {
			t.Fatalf("%s poster = %q after the re-cache", name, got.PosterPath)
		}
		return oldKeys
	}
	republished := recache("republished", true)
	replaced := recache("replaced", false)
	for _, key := range append(append([]string(nil), republished...), replaced...) {
		if _, err := store.Stat(ctx, key); err != nil {
			t.Fatalf("%s is gone before its grace period ended: %v", key, err)
		}
	}

	// End the grace period, then collect.
	if _, err := pool.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates SET not_before = NOW() - interval '1 second', next_attempt_at = NOW() - interval '1 second'
		WHERE original_path LIKE $1`, "local/movies/"+run+"-%"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewArtworkRevisionGarbageCollector(pool, store).Run(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range republished {
		if _, err := store.Stat(ctx, key); err != nil {
			t.Errorf("republished %s was deleted: %v", key, err)
		}
	}
	for _, key := range replaced {
		if _, err := store.Stat(ctx, key); err == nil {
			t.Errorf("replaced %s outlived its grace period", key)
		}
	}
}
