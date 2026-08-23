package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

type failingArtworkRevisionGCExecBeginner struct {
	pool *pgxpool.Pool
}

func (b failingArtworkRevisionGCExecBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &failingArtworkRevisionGCExecTx{Tx: tx}, nil
}

type failingArtworkRevisionGCExecTx struct {
	pgx.Tx
	execs int
}

func (tx *failingArtworkRevisionGCExecTx) Exec(
	ctx context.Context,
	sql string,
	args ...any,
) (pgconn.CommandTag, error) {
	tx.execs++
	if tx.execs == 2 {
		return pgconn.CommandTag{}, errors.New("injected heal lease failure")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestProcessArtworkRevisionGCBatchContinuesAfterRetryFailure(t *testing.T) {
	candidates := []artworkRevisionGCCandidate{{id: 1}, {id: 2}, {id: 3}, {id: 4}}
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
			case 3:
				return artworkRevisionGCDeletionPendingHeal, nil
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
	if !slices.Equal(processed, []int64{1, 2, 3, 4}) {
		t.Fatalf("processed candidates = %v, want all candidates", processed)
	}
	want := ArtworkRevisionGCStats{Claimed: 4, Deleted: 1, Referenced: 1, Retried: 1}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
}

func TestArtworkRevisionGCHealingSQLTargetsOneReferencedPath(t *testing.T) {
	surface := artworkSweepSurfaces()[0]
	query := artworkRevisionGCHealingSQL(surface, surface.resetSet(), surface.remoteSourcePredicate())
	for _, want := range []string{
		"UPDATE " + surface.table,
		"target." + surface.pathCol + " = $1",
		"candidate.id = $2",
		"candidate.deleted_at IS NOT NULL",
		"candidate.locked_by IN ('', $3)",
		surface.remoteSourcePredicate(),
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("heal query missing %q: %s", want, query)
		}
	}
	if strings.Contains(query, "unnest(") {
		t.Fatalf("exceptional healing must retain one-path lock width: %s", query)
	}
	if strings.Contains(query, "FOR UPDATE") {
		t.Fatalf("heal query must not invert source-to-candidate lock ordering: %s", query)
	}
}

func TestArtworkRevisionGCFinalizeSQLMaterializesReferenceSweep(t *testing.T) {
	query := artworkRevisionGCFinalizeSQL()
	for _, want := range []string{
		"WITH referenced AS MATERIALIZED",
		"unnest($1::bigint[], $2::text[])",
		"candidate.deleted_at IS NOT NULL",
		"candidate.locked_by IN ('', $3)",
		"NOT EXISTS",
		"RETURNING candidate.id",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("finalize query missing %q: %s", want, query)
		}
	}
	if got := strings.Count(query, " = ANY($2)"); got != len(artworkSweepSurfaces()) {
		t.Fatalf("final reference sweep covers %d surfaces, want %d", got, len(artworkSweepSurfaces()))
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

func TestArtworkRevisionGCBatchReferenceGateHealsRaces(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	workerID := fmt.Sprintf("gc-batch-heal-worker-%d", suffix)
	type healCase struct {
		contentID string
		path      string
		source    string
		want      string
		id        int64
	}
	cases := []healCase{
		{
			contentID: fmt.Sprintf("gc-batch-heal-remote-%d", suffix),
			path:      fmt.Sprintf("tmdb/movies/%d/poster/original.remote-gone.webp", suffix),
			source:    fmt.Sprintf("https://images.example/%d/poster.jpg", suffix),
			want:      fmt.Sprintf("https://images.example/%d/poster.jpg", suffix),
		},
		{
			contentID: fmt.Sprintf("gc-batch-heal-clear-%d", suffix),
			path:      fmt.Sprintf("tmdb/movies/%d/poster/original.local-gone.webp", suffix),
		},
	}
	contentIDs := make([]string, 0, len(cases))
	paths := make([]string, 0, len(cases))
	for i := range cases {
		item := &cases[i]
		contentIDs = append(contentIDs, item.contentID)
		paths = append(paths, item.path)
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (
				content_id, type, title, status, genres, poster_path, poster_source_path
			) VALUES ($1, 'movie', 'GC Batch Heal', 'matched', '{}'::text[], $2, $3)`,
			item.contentID, item.path, item.source); err != nil {
			t.Fatalf("seed batch heal item: %v", err)
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO artwork_revision_gc_candidates (
				original_path, image_type, object_keys, not_before, next_attempt_at,
				deleted_at, locked_at, locked_by
			) VALUES ($1, 'poster', '{}', NOW() - interval '1 hour', NOW() - interval '1 hour',
				NOW() - interval '1 hour', NOW(), $2)
			RETURNING id`, item.path, workerID).Scan(&item.id); err != nil {
			t.Fatalf("seed batch heal candidate: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, contentIDs)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, paths)
	})

	pending := make([]artworkRevisionGCPendingHeal, 0, len(cases))
	for _, item := range cases {
		pending = append(pending, artworkRevisionGCPendingHeal{
			candidate:    artworkRevisionGCCandidate{id: item.id, originalPath: item.path},
			originalPath: item.path,
		})
	}
	collector := NewArtworkRevisionGarbageCollector(pool, &blockingArtworkRevisionDeleter{started: make(chan struct{})})
	result, err := collector.finishPendingHeals(ctx, pending, workerID)
	if err != nil {
		t.Fatalf("finishPendingHeals: %v", err)
	}
	healed := result.healedPaths
	if len(healed) != len(cases) {
		t.Fatalf("healed paths = %v, want both deleted revisions", healed)
	}
	for _, item := range cases {
		if _, ok := healed[item.path]; !ok {
			t.Fatalf("healed paths missing %q: %v", item.path, healed)
		}
		var got string
		if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, item.contentID).Scan(&got); err != nil {
			t.Fatalf("load batch healed item: %v", err)
		}
		if got != item.want {
			t.Fatalf("poster_path = %q, want %q", got, item.want)
		}
	}
	var remaining int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, paths).Scan(&remaining); err != nil {
		t.Fatalf("count batch heal candidates: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("candidate rows remaining = %d, want 0", remaining)
	}
}

func TestArtworkRevisionGCHealStatementRollsBackBeforeRetry(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-heal-rollback-%d", suffix)
	originalPath := fmt.Sprintf("tmdb/movies/%d/poster/original.rollback.webp", suffix)
	sourcePath := fmt.Sprintf("https://images.example/%d/poster.jpg", suffix)
	workerID := fmt.Sprintf("gc-heal-rollback-worker-%d", suffix)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (
			content_id, type, title, status, genres, poster_path, poster_source_path
		) VALUES ($1, 'movie', 'GC Heal Rollback', 'matched', '{}'::text[], $2, $3)`,
		contentID, originalPath, sourcePath); err != nil {
		t.Fatalf("seed rollback item: %v", err)
	}
	var candidateID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, not_before, next_attempt_at,
			deleted_at, locked_at, locked_by
		) VALUES ($1, 'poster', '{}', NOW() - interval '1 hour', NOW() - interval '1 hour',
			NOW() - interval '1 hour', NOW(), $2)
		RETURNING id`, originalPath, workerID).Scan(&candidateID); err != nil {
		t.Fatalf("seed rollback candidate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath)
	})

	surface := artworkSweepSurfaces()[0]
	query := artworkRevisionGCHealingSQL(surface, surface.resetSet(), surface.remoteSourcePredicate())
	healed := make(map[string]struct{})
	err := healArtworkRevisionRows(
		ctx,
		failingArtworkRevisionGCExecBeginner{pool: pool},
		surface,
		query,
		artworkRevisionGCPendingHeal{
			candidate:    artworkRevisionGCCandidate{id: candidateID, originalPath: originalPath},
			originalPath: originalPath,
		},
		workerID,
		healed,
	)
	if err == nil || !strings.Contains(err.Error(), "injected heal lease failure") {
		t.Fatalf("heal error = %v, want injected lease failure", err)
	}
	if len(healed) != 0 {
		t.Fatalf("rolled-back heal reported success: %v", healed)
	}

	var posterPath, lockedBy string
	var nextAttempt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT item.poster_path, candidate.locked_by, candidate.next_attempt_at
		FROM media_items AS item
		JOIN artwork_revision_gc_candidates AS candidate ON candidate.original_path = item.poster_path
		WHERE item.content_id = $1`, contentID).Scan(&posterPath, &lockedBy, &nextAttempt); err != nil {
		t.Fatalf("load rolled-back state: %v", err)
	}
	if posterPath != originalPath {
		t.Fatalf("poster_path = %q, want rolled-back path %q", posterPath, originalPath)
	}
	if lockedBy != workerID {
		t.Fatalf("locked_by = %q, want original worker lease", lockedBy)
	}
	if nextAttempt.After(time.Now()) {
		t.Fatalf("next_attempt_at = %v, want original due time after rollback", nextAttempt)
	}

	collector := NewArtworkRevisionGarbageCollector(pool, &blockingArtworkRevisionDeleter{started: make(chan struct{})})
	retried, retryErr := collector.retryDeletedCandidate(ctx, artworkRevisionGCPendingHeal{
		candidate: artworkRevisionGCCandidate{
			id:           candidateID,
			originalPath: originalPath,
		},
		originalPath: originalPath,
	}, workerID, err)
	if retryErr != nil {
		t.Fatalf("retryDeletedCandidate: %v", retryErr)
	}
	if !retried {
		t.Fatal("rolled-back heal was not scheduled for retry")
	}
	var notBefore time.Time
	if err := pool.QueryRow(ctx, `
		SELECT not_before, next_attempt_at, locked_by
		FROM artwork_revision_gc_candidates
		WHERE id = $1`, candidateID).Scan(&notBefore, &nextAttempt, &lockedBy); err != nil {
		t.Fatalf("load retry state: %v", err)
	}
	if notBefore.After(time.Now()) {
		t.Fatalf("not_before = %v, want immediate heal eligibility", notBefore)
	}
	if nextAttempt.After(time.Now().Add(2 * time.Minute)) {
		t.Fatalf("next_attempt_at = %v, want bounded retry delay", nextAttempt)
	}
	if lockedBy != "" {
		t.Fatalf("locked_by = %q, want retry lease released", lockedBy)
	}
}

func TestArtworkRevisionGCFinalGuardRearmsVisibleReference(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-final-guard-%d", suffix)
	originalPath := fmt.Sprintf("tmdb/movies/%d/poster/original.final-guard.webp", suffix)
	workerID := fmt.Sprintf("gc-final-guard-worker-%d", suffix)

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path)
		VALUES ($1, 'movie', 'GC Final Guard', 'matched', '{}'::text[], $2)`, contentID, originalPath); err != nil {
		t.Fatalf("seed final guard item: %v", err)
	}
	var candidateID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, not_before, next_attempt_at,
			deleted_at, locked_at, locked_by
		) VALUES ($1, 'poster', '{}', NOW() - interval '1 hour', NOW() - interval '1 hour',
			NOW() - interval '1 hour', NOW(), $2)
		RETURNING id`, originalPath, workerID).Scan(&candidateID); err != nil {
		t.Fatalf("seed final guard candidate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath)
	})

	collector := NewArtworkRevisionGarbageCollector(pool, &blockingArtworkRevisionDeleter{started: make(chan struct{})})
	finalizedIDs, rearmedIDs, err := collector.finalizePendingHeals(
		ctx,
		[]int64{candidateID},
		[]string{originalPath},
		workerID,
		true,
	)
	if err != nil {
		t.Fatalf("finalizePendingHeals: %v", err)
	}
	if len(finalizedIDs) != 0 {
		t.Fatalf("visible reference finalized as unreferenced: %v", finalizedIDs)
	}
	if _, ok := rearmedIDs[candidateID]; !ok {
		t.Fatalf("guarded candidate was not rearmed: %v", rearmedIDs)
	}

	var deletedAt *time.Time
	var nextAttempt time.Time
	var lockedBy string
	if err := pool.QueryRow(ctx, `
		SELECT deleted_at, next_attempt_at, locked_by
		FROM artwork_revision_gc_candidates
		WHERE id = $1`, candidateID).Scan(&deletedAt, &nextAttempt, &lockedBy); err != nil {
		t.Fatalf("load guarded candidate: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("guarded candidate lost its durable deleted_at marker")
	}
	if nextAttempt.After(time.Now()) {
		t.Fatalf("next_attempt_at = %v, want immediate follow-up", nextAttempt)
	}
	if lockedBy != "" {
		t.Fatalf("locked_by = %q, want guarded candidate released", lockedBy)
	}
}

func TestArtworkRevisionGCBatchHealPreservesRetrackedRevision(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-batch-heal-retracked-%d", suffix)
	originalPath := fmt.Sprintf("tmdb/movies/%d/poster/original.retracked.webp", suffix)
	workerID := fmt.Sprintf("gc-batch-heal-retracked-worker-%d", suffix)
	newKeys := []string{originalPath, fmt.Sprintf("tmdb/movies/%d/poster/w500.retracked.webp", suffix)}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path)
		VALUES ($1, 'movie', 'GC Retracked Heal', 'matched', '{}'::text[], $2)`, contentID, originalPath); err != nil {
		t.Fatalf("seed retracked item: %v", err)
	}
	var candidateID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, not_before, next_attempt_at,
			deleted_at, locked_at, locked_by
		) VALUES ($1, 'poster', '{}', NOW() - interval '1 hour', NOW() - interval '1 hour',
			NOW() - interval '1 hour', NOW(), $2)
		RETURNING id`, originalPath, workerID).Scan(&candidateID); err != nil {
		t.Fatalf("seed retracked candidate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath)
	})

	tracker := catalog.NewArtworkRevisionTracker(pool)
	if err := tracker.TrackArtworkRevision(ctx, originalPath, "poster", newKeys); err != nil {
		t.Fatalf("retrack revision: %v", err)
	}
	collector := NewArtworkRevisionGarbageCollector(pool, &blockingArtworkRevisionDeleter{started: make(chan struct{})})
	result, err := collector.finishPendingHeals(ctx, []artworkRevisionGCPendingHeal{{
		candidate:    artworkRevisionGCCandidate{id: candidateID, originalPath: originalPath},
		originalPath: originalPath,
	}}, workerID)
	if err != nil {
		t.Fatalf("finishPendingHeals: %v", err)
	}
	if len(result.healedPaths) != 0 || len(result.finalizedIDs) != 0 {
		t.Fatalf("retracked revision was healed or finalized: %+v", result)
	}

	var posterPath string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, contentID).Scan(&posterPath); err != nil {
		t.Fatalf("load retracked item: %v", err)
	}
	if posterPath != originalPath {
		t.Fatalf("poster_path = %q, want retracked path %q preserved", posterPath, originalPath)
	}
	var deletedAt *time.Time
	var objectKeys []string
	if err := pool.QueryRow(ctx, `
		SELECT deleted_at, object_keys
		FROM artwork_revision_gc_candidates
		WHERE id = $1`, candidateID).Scan(&deletedAt, &objectKeys); err != nil {
		t.Fatalf("load retracked candidate: %v", err)
	}
	if deletedAt != nil {
		t.Fatalf("deleted_at = %v, want retracker to clear it", *deletedAt)
	}
	if !slices.Equal(objectKeys, newKeys) {
		t.Fatalf("object_keys = %v, want retracked manifest %v", objectKeys, newKeys)
	}
}

func TestArtworkRevisionGCBatchHealRechecksRetrackBetweenSurfaces(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contentID := fmt.Sprintf("gc-batch-heal-mid-retrack-%d", suffix)
	originalPath := fmt.Sprintf("tmdb/movies/%d/poster/original.mid-retrack.webp", suffix)
	posterSource := fmt.Sprintf("https://images.example/%d/poster.jpg", suffix)
	backdropSource := fmt.Sprintf("https://images.example/%d/backdrop.jpg", suffix)
	workerID := fmt.Sprintf("gc-batch-heal-mid-retrack-worker-%d", suffix)
	newKeys := []string{originalPath, fmt.Sprintf("tmdb/movies/%d/poster/w500.mid-retrack.webp", suffix)}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (
			content_id, type, title, status, genres, poster_path, poster_source_path,
			backdrop_path, backdrop_source_path
		) VALUES ($1, 'movie', 'GC Mid-Heal Retrack', 'matched', '{}'::text[], $2, $3, $2, $4)`,
		contentID, originalPath, posterSource, backdropSource); err != nil {
		t.Fatalf("seed mid-heal retrack item: %v", err)
	}
	var candidateID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, not_before, next_attempt_at,
			deleted_at, locked_at, locked_by
		) VALUES ($1, 'poster', '{}', NOW() - interval '1 hour', NOW() - interval '1 hour',
			NOW() - interval '1 hour', NOW(), $2)
		RETURNING id`, originalPath, workerID).Scan(&candidateID); err != nil {
		t.Fatalf("seed mid-heal retrack candidate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, originalPath)
	})

	posterSurface := artworkSweepSurfaces()[0]
	posterReset := artworkRevisionGCHealingSQL(
		posterSurface,
		posterSurface.resetSet(),
		posterSurface.remoteSourcePredicate(),
	)
	firstHealed := make(map[string]struct{})
	if err := healArtworkRevisionRows(
		ctx,
		pool,
		posterSurface,
		posterReset,
		artworkRevisionGCPendingHeal{
			candidate:    artworkRevisionGCCandidate{id: candidateID, originalPath: originalPath},
			originalPath: originalPath,
		},
		workerID,
		firstHealed,
	); err != nil {
		t.Fatalf("heal first surface: %v", err)
	}
	if _, ok := firstHealed[originalPath]; !ok {
		t.Fatalf("first surface did not heal %q", originalPath)
	}

	tracker := catalog.NewArtworkRevisionTracker(pool)
	if err := tracker.TrackArtworkRevision(ctx, originalPath, "poster", newKeys); err != nil {
		t.Fatalf("retrack between heal surfaces: %v", err)
	}
	collector := NewArtworkRevisionGarbageCollector(pool, &blockingArtworkRevisionDeleter{started: make(chan struct{})})
	result, err := collector.finishPendingHeals(ctx, []artworkRevisionGCPendingHeal{{
		candidate:    artworkRevisionGCCandidate{id: candidateID, originalPath: originalPath},
		originalPath: originalPath,
	}}, workerID)
	if err != nil {
		t.Fatalf("finishPendingHeals after retrack: %v", err)
	}
	if len(result.healedPaths) != 0 || len(result.finalizedIDs) != 0 {
		t.Fatalf("later surfaces ignored cleared deleted_at: %+v", result)
	}

	var posterPath, backdropPath string
	if err := pool.QueryRow(ctx, `
		SELECT poster_path, backdrop_path
		FROM media_items
		WHERE content_id = $1`, contentID).Scan(&posterPath, &backdropPath); err != nil {
		t.Fatalf("load mid-heal retrack item: %v", err)
	}
	if posterPath != posterSource {
		t.Fatalf("poster_path = %q, want first surface reset %q", posterPath, posterSource)
	}
	if backdropPath != originalPath {
		t.Fatalf("backdrop_path = %q, want retracked path %q preserved", backdropPath, originalPath)
	}
	var deletedAt *time.Time
	var objectKeys []string
	if err := pool.QueryRow(ctx, `
		SELECT deleted_at, object_keys
		FROM artwork_revision_gc_candidates
		WHERE id = $1`, candidateID).Scan(&deletedAt, &objectKeys); err != nil {
		t.Fatalf("load mid-heal retracked candidate: %v", err)
	}
	if deletedAt != nil {
		t.Fatalf("deleted_at = %v, want retracker to clear it", *deletedAt)
	}
	if !slices.Equal(objectKeys, newKeys) {
		t.Fatalf("object_keys = %v, want retracked manifest %v", objectKeys, newKeys)
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
