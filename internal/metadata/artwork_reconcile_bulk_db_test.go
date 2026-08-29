package metadata

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// shrinkBulkBatchSize forces the bulk-reset loops through multiple batches with
// a handful of seeded rows instead of thousands.
func shrinkBulkBatchSize(t *testing.T, size int) {
	t.Helper()
	prev := artworkReconcileBulkBatchSize
	artworkReconcileBulkBatchSize = size
	t.Cleanup(func() { artworkReconcileBulkBatchSize = prev })
}

func itemPosterSurface(t *testing.T) artworkSweepSurface {
	t.Helper()
	for _, s := range artworkSweepSurfaces() {
		if s.name == "item posters" {
			return s
		}
	}
	t.Fatal("item posters surface not found")
	return artworkSweepSurface{}
}

// The bulk resets under test sweep their whole table, not just this test's
// fixtures. The test database may be shared and populated, so each test
// snapshots every pre-existing row its reset would touch and restores those
// rows on cleanup; only the seeded fixtures are asserted on.

// restoreImageLadderState snapshots the image-ladder backfill singleton and
// restores it on cleanup. Inserting rows with local cached poster paths fires
// the reopen_image_ladder_backfill_v2 trigger, which on a database that has
// completed ladder v2 lowers backfilled_version — durable state a test
// fixture must not leave behind on a shared database.
func restoreImageLadderState(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var version int
	var lastAttempt *time.Time
	err := pool.QueryRow(ctx,
		`SELECT backfilled_version, last_attempt_at FROM image_ladder_backfill_state WHERE id = 1`).
		Scan(&version, &lastAttempt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		t.Fatalf("snapshot image ladder state: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx,
			`UPDATE image_ladder_backfill_state SET backfilled_version = $1, last_attempt_at = $2, updated_at = NOW() WHERE id = 1`,
			version, lastAttempt); err != nil {
			t.Errorf("restore image ladder state: %v", err)
		}
	})
}

// restoreDisplacedGCCandidates snapshots the artwork-GC candidate rows the
// bulk reset can touch and restores them on cleanup. Displacing a cached
// poster path ending in /original.<ext> fires queue_displaced_artwork_revision,
// which inserts a candidate for the path or resets an existing candidate's
// schedule, attempts, lease, and error state — auxiliary shared-database state
// the media_items row restore alone does not undo. Candidates created for the
// test's own fixture paths are deleted outright.
func restoreDisplacedGCCandidates(t *testing.T, pool *pgxpool.Pool, seededPathPattern string) {
	t.Helper()
	ctx := context.Background()
	surface := itemPosterSurface(t)

	var displaced []string
	rows, err := pool.Query(ctx, fmt.Sprintf(
		`SELECT poster_path FROM media_items WHERE %s`, surface.cachedPredicate()))
	if err != nil {
		t.Fatalf("list displaceable poster paths: %v", err)
	}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatalf("scan displaceable path: %v", err)
		}
		displaced = append(displaced, path)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read displaceable paths: %v", err)
	}

	type candidate struct {
		originalPath  string
		imageType     string
		objectKeys    []string
		notBefore     time.Time
		nextAttemptAt *time.Time
		deletedAt     *time.Time
		attemptCount  int
		lockedAt      *time.Time
		lockedBy      string
		lastError     string
	}
	var snapshot []candidate
	snapshotPaths := []string{}
	rows, err = pool.Query(ctx, `
		SELECT original_path, image_type, object_keys, not_before, next_attempt_at,
			deleted_at, attempt_count, locked_at, locked_by, last_error
		FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, displaced)
	if err != nil {
		t.Fatalf("snapshot gc candidates: %v", err)
	}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.originalPath, &c.imageType, &c.objectKeys, &c.notBefore,
			&c.nextAttemptAt, &c.deletedAt, &c.attemptCount, &c.lockedAt, &c.lockedBy, &c.lastError); err != nil {
			t.Fatalf("scan gc candidate: %v", err)
		}
		snapshot = append(snapshot, c)
		snapshotPaths = append(snapshotPaths, c.originalPath)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read gc candidates: %v", err)
	}

	t.Cleanup(func() {
		if _, err := pool.Exec(ctx,
			`DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, seededPathPattern); err != nil {
			t.Errorf("delete fixture gc candidates: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			DELETE FROM artwork_revision_gc_candidates
			WHERE original_path = ANY($1) AND original_path <> ALL($2)`, displaced, snapshotPaths); err != nil {
			t.Errorf("delete reset-created gc candidates: %v", err)
		}
		for _, c := range snapshot {
			if _, err := pool.Exec(ctx, `
				INSERT INTO artwork_revision_gc_candidates (
					original_path, image_type, object_keys, not_before, next_attempt_at,
					deleted_at, attempt_count, locked_at, locked_by, last_error, updated_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
				ON CONFLICT (original_path) DO UPDATE SET
					image_type = EXCLUDED.image_type,
					object_keys = EXCLUDED.object_keys,
					not_before = EXCLUDED.not_before,
					next_attempt_at = EXCLUDED.next_attempt_at,
					deleted_at = EXCLUDED.deleted_at,
					attempt_count = EXCLUDED.attempt_count,
					locked_at = EXCLUDED.locked_at,
					locked_by = EXCLUDED.locked_by,
					last_error = EXCLUDED.last_error,
					updated_at = NOW()`,
				c.originalPath, c.imageType, c.objectKeys, c.notBefore, c.nextAttemptAt,
				c.deletedAt, c.attemptCount, c.lockedAt, c.lockedBy, c.lastError); err != nil {
				t.Errorf("restore gc candidate %s: %v", c.originalPath, err)
			}
		}
	})
}

func restorePreexistingPosterRows(t *testing.T, pool *pgxpool.Pool, seededPrefix string) {
	t.Helper()
	ctx := context.Background()
	surface := itemPosterSurface(t)
	rows, err := pool.Query(ctx, fmt.Sprintf(
		`SELECT content_id, poster_path, last_refreshed, updated_at FROM media_items
		 WHERE %s AND content_id NOT LIKE $1`, surface.cachedPredicate()), seededPrefix+"%")
	if err != nil {
		t.Fatalf("snapshot pre-existing poster rows: %v", err)
	}
	type itemState struct {
		contentID     string
		posterPath    string
		lastRefreshed *time.Time
		updatedAt     *time.Time
	}
	var snapshot []itemState
	for rows.Next() {
		var s itemState
		if err := rows.Scan(&s.contentID, &s.posterPath, &s.lastRefreshed, &s.updatedAt); err != nil {
			t.Fatalf("scan poster snapshot: %v", err)
		}
		snapshot = append(snapshot, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read poster snapshot: %v", err)
	}
	t.Cleanup(func() {
		for _, s := range snapshot {
			if _, err := pool.Exec(ctx,
				`UPDATE media_items SET poster_path = $2, last_refreshed = $3, updated_at = $4 WHERE content_id = $1`,
				s.contentID, s.posterPath, s.lastRefreshed, s.updatedAt); err != nil {
				t.Errorf("restore poster row %s: %v", s.contentID, err)
			}
		}
	})
}

func restorePreexistingChapterRows(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx,
		`SELECT id, chapters, chapter_thumbnail_retry_after FROM media_files WHERE `+chapterThumbnailFilesPredicate)
	if err != nil {
		t.Fatalf("snapshot pre-existing chapter rows: %v", err)
	}
	type fileState struct {
		id         int64
		chapters   []byte
		retryAfter *time.Time
	}
	var snapshot []fileState
	for rows.Next() {
		var s fileState
		if err := rows.Scan(&s.id, &s.chapters, &s.retryAfter); err != nil {
			t.Fatalf("scan chapter snapshot: %v", err)
		}
		snapshot = append(snapshot, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read chapter snapshot: %v", err)
	}
	t.Cleanup(func() {
		for _, s := range snapshot {
			if _, err := pool.Exec(ctx,
				`UPDATE media_files SET chapters = $2::jsonb, chapter_thumbnail_retry_after = $3 WHERE id = $1`,
				s.id, s.chapters, s.retryAfter); err != nil {
				t.Errorf("restore chapter row %d: %v", s.id, err)
			}
		}
	})
}

// TestBulkResetSurfaceBatches drives bulkResetSurface across several batches
// and verifies both halves: rows with a remote source are requeued (path reset
// to the source URL), rows without one are cleared. Assertions are scoped to
// the seeded rows; pre-existing rows the sweep touches are snapshot-restored.
func TestBulkResetSurfaceBatches(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	shrinkBulkBatchSize(t, 3)

	prefix := fmt.Sprintf("bulkreset-%d", time.Now().UnixNano())
	restoreImageLadderState(t, pool)
	restoreDisplacedGCCandidates(t, pool, "metadata/movie/"+prefix+"-%")
	restorePreexistingPosterRows(t, pool, prefix)
	const requeueRows, clearRows = 7, 5
	sourceURL := func(i int) string {
		return fmt.Sprintf("https://image.example.org/t/p/original/%s-%d.jpg", prefix, i)
	}

	var seeded []string
	for i := 0; i < requeueRows+clearRows; i++ {
		contentID := fmt.Sprintf("%s-%d", prefix, i)
		source := ""
		if i < requeueRows {
			source = sourceURL(i)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path, last_refreshed)
			VALUES ($1, 'movie', 'Bulk Reset Test', 'matched', '{}'::text[], $2, $3, NOW())
		`, contentID, fmt.Sprintf("metadata/movie/%s/poster/original.webp", contentID), source); err != nil {
			t.Fatalf("seed item %d: %v", i, err)
		}
		seeded = append(seeded, contentID)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"-%")
	})

	r := &ArtworkCacheReconciler{pool: pool}
	var stats ArtworkReconcileStats
	if err := r.bulkResetSurface(ctx, itemPosterSurface(t), &stats); err != nil {
		t.Fatalf("bulkResetSurface: %v", err)
	}
	if stats.Requeued < requeueRows {
		t.Errorf("stats.Requeued = %d, want >= %d", stats.Requeued, requeueRows)
	}
	if stats.Cleared < clearRows {
		t.Errorf("stats.Cleared = %d, want >= %d", stats.Cleared, clearRows)
	}

	for i, contentID := range seeded {
		var posterPath string
		var lastRefreshed *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT poster_path, last_refreshed FROM media_items WHERE content_id = $1`,
			contentID).Scan(&posterPath, &lastRefreshed); err != nil {
			t.Fatalf("read back item %d: %v", i, err)
		}
		if i < requeueRows {
			if posterPath != sourceURL(i) {
				t.Errorf("row %d: poster_path = %q, want requeued source %q", i, posterPath, sourceURL(i))
			}
		} else {
			if posterPath != "" {
				t.Errorf("row %d: poster_path = %q, want cleared", i, posterPath)
			}
			if lastRefreshed != nil {
				t.Errorf("row %d: last_refreshed = %v, want NULL after clear", i, lastRefreshed)
			}
		}
	}
}

// TestBulkResetChapterThumbnailsBatches drives the chapter-thumbnail reset
// across several batches and verifies the JSONB rewrite: cached thumbnail
// elements are emptied and stripped of retry state, elements without a
// thumbnail survive untouched, and the loop terminates once no row matches.
func TestBulkResetChapterThumbnailsBatches(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	shrinkBulkBatchSize(t, 2)
	restorePreexistingChapterRows(t, pool)

	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name)
		VALUES ('movies', $1)
		RETURNING id`, fmt.Sprintf("bulk-chapter-test-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	const fileRows = 5
	fileIDs := make([]int, fileRows)
	for i := range fileIDs {
		chapters := fmt.Sprintf(`[
			{"title": "c1", "thumbnail_path": "chapters/%d-%d/1.webp", "thumbnail_thumbhash": "aa", "thumbnail_retry_after": "2026-01-01T00:00:00Z", "thumbnail_failed_at": "2026-01-01T00:00:00Z", "thumbnail_last_error": "boom"},
			{"title": "c2"}
		]`, suffix, i)
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, chapters, chapter_thumbnail_retry_after)
			VALUES ($1, $2, $3::jsonb, NOW())
			RETURNING id`, folderID, fmt.Sprintf("/bulk-chapter-test/%d-%d.mkv", suffix, i), chapters).Scan(&fileIDs[i]); err != nil {
			t.Fatalf("seed media file %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = ANY($1)`, fileIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	r := &ArtworkCacheReconciler{pool: pool}
	var stats ArtworkReconcileStats
	if err := r.bulkResetChapterThumbnails(ctx, &stats); err != nil {
		t.Fatalf("bulkResetChapterThumbnails: %v", err)
	}
	if stats.Cleared < fileRows {
		t.Errorf("stats.Cleared = %d, want >= %d", stats.Cleared, fileRows)
	}

	for i, id := range fileIDs {
		var first, second map[string]any
		var retryAfter *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT chapters->0, chapters->1, chapter_thumbnail_retry_after FROM media_files WHERE id = $1`,
			id).Scan(&first, &second, &retryAfter); err != nil {
			t.Fatalf("read back file %d: %v", i, err)
		}
		if got := first["thumbnail_path"]; got != "" {
			t.Errorf("file %d: thumbnail_path = %v, want emptied", i, got)
		}
		if got := first["thumbnail_thumbhash"]; got != "" {
			t.Errorf("file %d: thumbnail_thumbhash = %v, want emptied", i, got)
		}
		for _, key := range []string{"thumbnail_retry_after", "thumbnail_failed_at", "thumbnail_last_error"} {
			if _, ok := first[key]; ok {
				t.Errorf("file %d: %s survived the reset", i, key)
			}
		}
		if got := second["title"]; got != "c2" {
			t.Errorf("file %d: untouched element title = %v, want c2", i, got)
		}
		if _, ok := second["thumbnail_path"]; ok {
			t.Errorf("file %d: element without a thumbnail gained thumbnail_path", i)
		}
		if retryAfter != nil {
			t.Errorf("file %d: chapter_thumbnail_retry_after = %v, want NULL", i, retryAfter)
		}
	}
}

// TestBulkResetSurfacePartialFailureKeepsCounts interrupts bulkResetSurface
// after its requeue phase by giving the clear phase a broken SET clause.
// Batches commit as they go and the task serializes stats on failure, so the
// rows the requeue phase durably changed must be counted despite the error.
func TestBulkResetSurfacePartialFailureKeepsCounts(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := context.Background()
	shrinkBulkBatchSize(t, 2)

	prefix := fmt.Sprintf("bulkpartial-%d", time.Now().UnixNano())
	restoreImageLadderState(t, pool)
	restoreDisplacedGCCandidates(t, pool, "metadata/movie/"+prefix+"-%")
	restorePreexistingPosterRows(t, pool, prefix)

	const requeueRows = 5
	for i := 0; i < requeueRows; i++ {
		contentID := fmt.Sprintf("%s-%d", prefix, i)
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
			VALUES ($1, 'movie', 'Bulk Partial Test', 'matched', '{}'::text[], $2, $3)
		`, contentID, fmt.Sprintf("metadata/movie/%s/poster/original.webp", contentID),
			fmt.Sprintf("https://image.example.org/t/p/original/%s-%d.jpg", prefix, i)); err != nil {
			t.Fatalf("seed item %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"-%")
	})

	surface := itemPosterSurface(t)
	surface.clearSet = `nonexistent_bulk_partial_column = ''`

	r := &ArtworkCacheReconciler{pool: pool}
	var stats ArtworkReconcileStats
	err := r.bulkResetSurface(ctx, surface, &stats)
	if err == nil {
		t.Fatal("bulkResetSurface succeeded, want the clear phase to fail")
	}
	if stats.Requeued < requeueRows {
		t.Errorf("stats.Requeued = %d after clear-phase failure, want >= %d committed requeues", stats.Requeued, requeueRows)
	}
}

func TestRetryOnDeadlock(t *testing.T) {
	prevBackoff := artworkReconcileDeadlockBaseBackoff
	artworkReconcileDeadlockBaseBackoff = time.Millisecond
	t.Cleanup(func() { artworkReconcileDeadlockBaseBackoff = prevBackoff })

	deadlock := &pgconn.PgError{Code: "40P01"}
	ctx := context.Background()

	t.Run("retries deadlocks until success", func(t *testing.T) {
		calls := 0
		err := retryOnDeadlock(ctx, func() error {
			calls++
			if calls < 3 {
				return fmt.Errorf("exec: %w", deadlock)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("retryOnDeadlock: %v", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		calls := 0
		err := retryOnDeadlock(ctx, func() error {
			calls++
			return deadlock
		})
		if !errors.Is(err, deadlock) {
			t.Fatalf("err = %v, want the deadlock error", err)
		}
		if calls != artworkReconcileDeadlockMaxAttempts {
			t.Errorf("calls = %d, want %d", calls, artworkReconcileDeadlockMaxAttempts)
		}
	})

	t.Run("returns other errors immediately", func(t *testing.T) {
		calls := 0
		boom := errors.New("boom")
		if err := retryOnDeadlock(ctx, func() error {
			calls++
			return boom
		}); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("honors cancellation between attempts", func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		err := retryOnDeadlock(canceled, func() error { return deadlock })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}
