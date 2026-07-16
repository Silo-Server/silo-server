package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

type blockingArtworkRevisionDeleter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu      sync.Mutex
	deleted [][]string
}

func (d *blockingArtworkRevisionDeleter) Bucket() string { return "artwork" }

func (d *blockingArtworkRevisionDeleter) DeleteObjects(ctx context.Context, _ string, keys []string) (int, error) {
	d.once.Do(func() { close(d.started) })
	if d.release != nil {
		select {
		case <-d.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	d.mu.Lock()
	d.deleted = append(d.deleted, append([]string(nil), keys...))
	d.mu.Unlock()
	return len(keys), nil
}

func artworkRevisionGCTestPool(t *testing.T) *pgxpool.Pool {
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

func TestProcessArtworkRevisionGCBatchContinuesAfterRetryFailure(t *testing.T) {
	candidates := []artworkRevisionGCCandidate{{id: 1}, {id: 2}, {id: 3}}
	processed := make([]int64, 0, len(candidates))
	retryErr := errors.New("schedule retry")

	stats, err := processArtworkRevisionGCBatch(
		candidates,
		func(candidate artworkRevisionGCCandidate) (artworkRevisionGCOutcome, error) {
			processed = append(processed, candidate.id)
			switch candidate.id {
			case 1:
				return artworkRevisionGCSuperseded, errors.New("delete object")
			case 2:
				return artworkRevisionGCReferenced, nil
			default:
				return artworkRevisionGCDeleted, nil
			}
		},
		func(candidate artworkRevisionGCCandidate, _ error) error {
			if candidate.id == 1 {
				return retryErr
			}
			return nil
		},
	)

	if !errors.Is(err, retryErr) {
		t.Fatalf("process batch error = %v, want %v", err, retryErr)
	}
	if !slices.Equal(processed, []int64{1, 2, 3}) {
		t.Fatalf("processed candidates = %v, want all candidates", processed)
	}
	want := ArtworkRevisionGCStats{Claimed: 3, Deleted: 1, Referenced: 1, Retried: 1}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
}

func TestArtworkRevisionGCSerializesDeletionWithRetracking(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	originalPath := fmt.Sprintf("tmdb/movies/gc-%d/poster/original.old.webp", suffix)
	oldKeys := []string{originalPath, fmt.Sprintf("tmdb/movies/gc-%d/poster/w500.old.webp", suffix)}
	newKeys := []string{originalPath, fmt.Sprintf("tmdb/movies/gc-%d/poster/w500.old.webp", suffix), fmt.Sprintf("tmdb/movies/gc-%d/poster/w300.old.webp", suffix)}
	workerID := fmt.Sprintf("gc-worker-%d", suffix)

	var candidateID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, object_keys, not_before, next_attempt_at, locked_at, locked_by
		) VALUES ($1, $2, NOW() - interval '1 hour', NOW() - interval '1 hour', NOW(), $3)
		RETURNING id`, originalPath, oldKeys, workerID).Scan(&candidateID); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath)
	})

	deleter := &blockingArtworkRevisionDeleter{started: make(chan struct{}), release: make(chan struct{})}
	collector := NewArtworkRevisionGarbageCollector(pool, deleter)
	processDone := make(chan struct {
		outcome artworkRevisionGCOutcome
		err     error
	}, 1)
	go func() {
		outcome, err := collector.processCandidate(ctx, artworkRevisionGCCandidate{
			id: candidateID, originalPath: originalPath, objectKeys: oldKeys,
		}, workerID)
		processDone <- struct {
			outcome artworkRevisionGCOutcome
			err     error
		}{outcome: outcome, err: err}
	}()

	select {
	case <-deleter.started:
	case <-ctx.Done():
		t.Fatalf("collector did not begin deletion: %v", ctx.Err())
	}

	tracker := catalog.NewArtworkRevisionTracker(pool)
	trackDone := make(chan error, 1)
	go func() { trackDone <- tracker.TrackArtworkRevision(ctx, originalPath, "poster", newKeys) }()

	select {
	case err := <-trackDone:
		t.Fatalf("tracking completed while deletion row was locked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(deleter.release)
	processed := <-processDone
	if processed.err != nil {
		t.Fatalf("processCandidate: %v", processed.err)
	}
	if processed.outcome != artworkRevisionGCDeleted {
		t.Fatalf("outcome = %v, want deleted", processed.outcome)
	}
	if err := <-trackDone; err != nil {
		t.Fatalf("retrack revision: %v", err)
	}

	var storedKeys []string
	var nextAttempt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT object_keys, next_attempt_at
		FROM artwork_revision_gc_candidates
		WHERE original_path = $1`, originalPath).Scan(&storedKeys, &nextAttempt); err != nil {
		t.Fatalf("load retracked revision: %v", err)
	}
	if !slices.Equal(storedKeys, newKeys) {
		t.Fatalf("stored manifest = %v, want %v", storedKeys, newKeys)
	}
	if !nextAttempt.After(time.Now().Add(23 * time.Hour)) {
		t.Fatalf("next attempt = %v, want refreshed publication grace", nextAttempt)
	}
}

func TestArtworkRevisionGCParksReferencedRevision(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-referenced-%d", suffix)
	originalPath := fmt.Sprintf("tmdb/movies/%d/poster/original.live.webp", suffix)
	keys := []string{originalPath}
	workerID := fmt.Sprintf("gc-worker-%d", suffix)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path)
		VALUES ($1, 'movie', 'GC Referenced', 'matched', '{}'::text[], $2)`, contentID, originalPath); err != nil {
		t.Fatalf("seed referenced item: %v", err)
	}
	var candidateID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, object_keys, not_before, next_attempt_at, locked_at, locked_by
		) VALUES ($1, $2, NOW() - interval '1 hour', NOW() - interval '1 hour', NOW(), $3)
		RETURNING id`, originalPath, keys, workerID).Scan(&candidateID); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath)
	})

	deleter := &blockingArtworkRevisionDeleter{started: make(chan struct{})}
	collector := NewArtworkRevisionGarbageCollector(pool, deleter)
	outcome, err := collector.processCandidate(ctx, artworkRevisionGCCandidate{id: candidateID}, workerID)
	if err != nil {
		t.Fatalf("processCandidate: %v", err)
	}
	if outcome != artworkRevisionGCReferenced {
		t.Fatalf("outcome = %v, want referenced", outcome)
	}
	select {
	case <-deleter.started:
		t.Fatal("referenced revision was sent to object deletion")
	default:
	}

	var lockedBy string
	var nextAttempt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT locked_by, next_attempt_at
		FROM artwork_revision_gc_candidates
		WHERE id = $1`, candidateID).Scan(&lockedBy, &nextAttempt); err != nil {
		t.Fatalf("load parked revision: %v", err)
	}
	if lockedBy != "" {
		t.Fatalf("locked_by = %q, want released", lockedBy)
	}
	if nextAttempt != nil {
		t.Fatalf("next_attempt_at = %v, want dormant NULL", nextAttempt)
	}
}

func TestArtworkRevisionTriggerQueuesDisplacedRevision(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-trigger-%d", suffix)
	oldPath := fmt.Sprintf("tmdb/movies/%d/poster/original.old.webp", suffix)
	newPath := fmt.Sprintf("tmdb/movies/%d/poster/original.new.webp", suffix)
	wantKeys := []string{
		oldPath,
		fmt.Sprintf("tmdb/movies/%d/poster/w500.old.webp", suffix),
		fmt.Sprintf("tmdb/movies/%d/poster/w300.old.webp", suffix),
		fmt.Sprintf("tmdb/movies/%d/poster/future-variant.old.webp", suffix),
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path)
		VALUES ($1, 'movie', 'GC Trigger', 'matched', '{}'::text[], $2)`, contentID, oldPath); err != nil {
		t.Fatalf("seed artwork: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, object_keys, not_before, next_attempt_at
		) VALUES ($1, $2, NOW(), NULL)`, oldPath, wantKeys); err != nil {
		t.Fatalf("seed dormant manifest: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path IN ($1, $2)`, oldPath, newPath)
	})

	if _, err := pool.Exec(ctx, `UPDATE media_items SET poster_path = $2 WHERE content_id = $1`, contentID, newPath); err != nil {
		t.Fatalf("replace artwork: %v", err)
	}

	var objectKeys []string
	var notBefore time.Time
	if err := pool.QueryRow(ctx, `
		SELECT object_keys, not_before
		FROM artwork_revision_gc_candidates
		WHERE original_path = $1`, oldPath).Scan(&objectKeys, &notBefore); err != nil {
		t.Fatalf("load displaced candidate: %v", err)
	}
	if !slices.Equal(objectKeys, wantKeys) {
		t.Fatalf("object manifest = %v, want %v", objectKeys, wantKeys)
	}
	if !notBefore.After(time.Now().Add(23 * time.Hour)) {
		t.Fatalf("not before = %v, want displacement grace period", notBefore)
	}
}

func TestArtworkRevisionTriggerRecordsImageTypeForCollectorExpansion(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-trigger-manifest-%d", suffix)
	imageTypes := []string{ImageCacheImagePoster, ImageCacheImageBackdrop, ImageCacheImageLogo}
	oldPaths := make(map[string]string, len(imageTypes))
	newPaths := make(map[string]string, len(imageTypes))
	for _, imageType := range imageTypes {
		oldPaths[imageType] = fmt.Sprintf(
			"tmdb/movies/original.segment/%d/%s/original.old.webp", suffix, imageType,
		)
		newPaths[imageType] = fmt.Sprintf("tmdb/movies/%d/%s/original.new.webp", suffix, imageType)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (
			content_id, type, title, status, genres, poster_path, backdrop_path, logo_path
		) VALUES ($1, 'movie', 'GC Trigger Manifest', 'matched', '{}'::text[], $2, $3, $4)`,
		contentID,
		oldPaths[ImageCacheImagePoster],
		oldPaths[ImageCacheImageBackdrop],
		oldPaths[ImageCacheImageLogo],
	); err != nil {
		t.Fatalf("seed artwork: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		paths := []string{
			oldPaths[ImageCacheImagePoster], oldPaths[ImageCacheImageBackdrop], oldPaths[ImageCacheImageLogo],
			newPaths[ImageCacheImagePoster], newPaths[ImageCacheImageBackdrop], newPaths[ImageCacheImageLogo],
		}
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, paths)
	})

	if _, err := pool.Exec(ctx, `
		UPDATE media_items
		SET poster_path = $2, backdrop_path = $3, logo_path = $4
		WHERE content_id = $1`,
		contentID,
		newPaths[ImageCacheImagePoster],
		newPaths[ImageCacheImageBackdrop],
		newPaths[ImageCacheImageLogo],
	); err != nil {
		t.Fatalf("replace artwork: %v", err)
	}

	// The trigger stores only the displaced path and its image type; the
	// collector expands the manifest via artworkkey at deletion time.
	for _, imageType := range imageTypes {
		var objectKeys []string
		var storedType string
		if err := pool.QueryRow(ctx, `
			SELECT object_keys, image_type
			FROM artwork_revision_gc_candidates
			WHERE original_path = $1`, oldPaths[imageType]).Scan(&objectKeys, &storedType); err != nil {
			t.Fatalf("load %s candidate: %v", imageType, err)
		}
		if len(objectKeys) != 0 {
			t.Fatalf("%s object manifest = %v, want trigger to leave expansion to the collector", imageType, objectKeys)
		}
		if storedType != imageType {
			t.Fatalf("stored image type = %q, want %q", storedType, imageType)
		}
	}
}

func TestArtworkRevisionTriggerIgnoresUnchangedAssignment(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-trigger-noop-%d", suffix)
	path := fmt.Sprintf("tmdb/movies/%d/poster/original.same.webp", suffix)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path)
		VALUES ($1, 'movie', 'GC Trigger Noop', 'matched', '{}'::text[], $2)`, contentID, path); err != nil {
		t.Fatalf("seed artwork: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, path)
	})

	// Upsert-style write assigning the same value must not queue anything.
	if _, err := pool.Exec(ctx, `UPDATE media_items SET poster_path = $2 WHERE content_id = $1`, contentID, path); err != nil {
		t.Fatalf("no-op update: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM artwork_revision_gc_candidates WHERE original_path = $1`, path).Scan(&count); err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	if count != 0 {
		t.Fatalf("unchanged assignment queued %d candidates, want 0", count)
	}
}

func TestArtworkRevisionGCExpandsTriggerManifestFromImageType(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	originalPath := fmt.Sprintf("tmdb/movies/gc-expand-%d/backdrop/original.old.webp", suffix)
	workerID := fmt.Sprintf("gc-worker-%d", suffix)

	var candidateID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, not_before, next_attempt_at, locked_at, locked_by
		) VALUES ($1, 'backdrop', '{}', NOW() - interval '1 hour', NOW() - interval '1 hour', NOW(), $2)
		RETURNING id`, originalPath, workerID).Scan(&candidateID); err != nil {
		t.Fatalf("seed trigger-style candidate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath)
	})

	deleter := &blockingArtworkRevisionDeleter{started: make(chan struct{})}
	collector := NewArtworkRevisionGarbageCollector(pool, deleter)
	outcome, err := collector.processCandidate(ctx, artworkRevisionGCCandidate{id: candidateID}, workerID)
	if err != nil {
		t.Fatalf("processCandidate: %v", err)
	}
	if outcome != artworkRevisionGCDeleted {
		t.Fatalf("outcome = %v, want deleted", outcome)
	}

	deleter.mu.Lock()
	deleted := deleter.deleted
	deleter.mu.Unlock()
	if len(deleted) != 1 {
		t.Fatalf("DeleteObjects calls = %d, want 1", len(deleted))
	}
	want := artworkkey.ObjectKeys(originalPath, "backdrop")
	if !slices.Equal(deleted[0], want) {
		t.Fatalf("deleted keys = %v, want expanded manifest %v", deleted[0], want)
	}
}

func TestArtworkRevisionGCHealsBrokenReferenceAfterObjectDeletion(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-heal-%d", suffix)
	originalPath := fmt.Sprintf("tmdb/movies/%d/poster/original.gone.webp", suffix)
	workerID := fmt.Sprintf("gc-worker-%d", suffix)

	// A racing writer re-referenced the path after its objects were deleted:
	// the candidate row survived with deleted_at set (heal previously failed).
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path)
		VALUES ($1, 'movie', 'GC Heal', 'matched', '{}'::text[], $2)`, contentID, originalPath); err != nil {
		t.Fatalf("seed re-referencing item: %v", err)
	}
	var candidateID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, not_before, next_attempt_at, deleted_at, locked_at, locked_by
		) VALUES ($1, 'poster', '{}', NOW() - interval '1 hour', NOW() - interval '1 hour', NOW() - interval '1 hour', NOW(), $2)
		RETURNING id`, originalPath, workerID).Scan(&candidateID); err != nil {
		t.Fatalf("seed deleted candidate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath)
	})

	deleter := &blockingArtworkRevisionDeleter{started: make(chan struct{})}
	collector := NewArtworkRevisionGarbageCollector(pool, deleter)
	outcome, err := collector.processCandidate(ctx, artworkRevisionGCCandidate{id: candidateID}, workerID)
	if err != nil {
		t.Fatalf("processCandidate: %v", err)
	}
	if outcome != artworkRevisionGCDeletedAndHealed {
		t.Fatalf("outcome = %v, want deleted-and-healed (broken reference must not park)", outcome)
	}

	var posterPath string
	if err := pool.QueryRow(ctx, `
		SELECT poster_path FROM media_items WHERE content_id = $1`, contentID).Scan(&posterPath); err != nil {
		t.Fatalf("load healed item: %v", err)
	}
	if posterPath == originalPath {
		t.Fatalf("poster_path still references deleted revision %q", posterPath)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath).Scan(&remaining); err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("candidate rows remaining = %d, want 0 after successful heal", remaining)
	}
}

func TestArtworkRevisionGCDormantSweep(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	referencedContentID := fmt.Sprintf("gc-dormant-ref-%d", suffix)
	referencedPath := fmt.Sprintf("tmdb/movies/%d/poster/original.ref.webp", suffix)
	orphanPath := fmt.Sprintf("tmdb/movies/%d/poster/original.orphan.webp", suffix)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path)
		VALUES ($1, 'movie', 'GC Dormant Ref', 'matched', '{}'::text[], $2)`, referencedContentID, referencedPath); err != nil {
		t.Fatalf("seed referenced item: %v", err)
	}
	for _, path := range []string{referencedPath, orphanPath} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO artwork_revision_gc_candidates (
				original_path, image_type, object_keys, not_before, next_attempt_at
			) VALUES ($1, 'poster', '{}', NOW() - interval '2 days', NULL)`, path); err != nil {
			t.Fatalf("seed dormant candidate: %v", err)
		}
	}
	// Age both rows past the recheck interval; a reference that vanished
	// through an untriggered surface looks exactly like the orphan row.
	if _, err := pool.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates
		SET updated_at = NOW() - interval '2 days'
		WHERE original_path = ANY($1)`, []string{referencedPath, orphanPath}); err != nil {
		t.Fatalf("age dormant candidates: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, referencedContentID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, []string{referencedPath, orphanPath})
	})

	deleter := &blockingArtworkRevisionDeleter{started: make(chan struct{})}
	collector := NewArtworkRevisionGarbageCollector(pool, deleter)
	checked, requeued, err := collector.sweepDormant(ctx, artworkRevisionGCBatchSize)
	if err != nil {
		t.Fatalf("sweepDormant: %v", err)
	}
	if checked < 2 {
		t.Fatalf("checked = %d, want at least the two seeded rows", checked)
	}
	if requeued < 1 {
		t.Fatalf("requeued = %d, want at least the orphan row", requeued)
	}

	var orphanNext, referencedNext *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at FROM artwork_revision_gc_candidates WHERE original_path = $1`, orphanPath).Scan(&orphanNext); err != nil {
		t.Fatalf("load orphan candidate: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at FROM artwork_revision_gc_candidates WHERE original_path = $1`, referencedPath).Scan(&referencedNext); err != nil {
		t.Fatalf("load referenced candidate: %v", err)
	}
	if orphanNext == nil {
		t.Fatal("orphan dormant row was not re-armed by the sweep")
	}
	if referencedNext != nil {
		t.Fatalf("referenced dormant row was re-armed: %v", *referencedNext)
	}
}
