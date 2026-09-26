package metadata

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type artworkRevisionDeleteFunc func(context.Context, []string) (int, error)

func (f artworkRevisionDeleteFunc) Delete(ctx context.Context, keys []string) (int, error) {
	return f(ctx, keys)
}

func TestArtworkRevisionGCRunLocksBatchDuringDeletion(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := t.Context()
	paths := []string{"tmdb/movies/gc-run-lock-1/poster/original.old.webp", "tmdb/movies/gc-run-lock-2/poster/original.old.webp"}
	for _, path := range paths {
		_, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates
   (original_path, object_keys, not_before, next_attempt_at)
   VALUES ($1, ARRAY[$1]::text[], NOW() - interval '1 hour', NOW() - interval '1 hour')`, path)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, paths)
	})
	calls := 0
	deleter := artworkRevisionDeleteFunc(func(ctx context.Context, keys []string) (int, error) {
		calls++
		if len(keys) != len(paths) {
			t.Errorf("delete keys = %v, want both revisions in one call", keys)
		}
		for _, path := range paths {
			tx, err := pool.Begin(ctx)
			if err != nil {
				return 0, err
			}
			_, lockErr := tx.Exec(ctx, `SELECT id FROM artwork_revision_gc_candidates WHERE original_path = $1 FOR UPDATE NOWAIT`, path)
			_ = tx.Rollback(ctx)
			pgErr, ok := errors.AsType[*pgconn.PgError](lockErr)
			if !ok || pgErr.Code != "55P03" {
				t.Errorf("revision %s is not locked during object deletion: %v", path, lockErr)
			}
		}
		return len(keys), nil
	})
	stats, err := NewArtworkRevisionGarbageCollector(pool, deleter).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || stats.Deleted != 2 {
		t.Fatalf("calls = %d, stats = %+v, want one call and two deletions", calls, stats)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, paths).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("remaining candidates = %d", remaining)
	}
}

func TestArtworkRevisionGCBatchRechecksClaimedState(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := t.Context()
	const workerID = "gc-batch-state"
	paths := []string{"gc-batch-referenced", "gc-batch-retracked", "gc-batch-manifest", "gc-batch-pending-heal"}
	var candidates []artworkRevisionGCCandidate
	for _, path := range paths {
		candidate := artworkRevisionGCCandidate{originalPath: path, objectKeys: []string{path + "/stale"}}
		err := pool.QueryRow(ctx, `INSERT INTO artwork_revision_gc_candidates
   (original_path, object_keys, not_before, next_attempt_at, locked_at, locked_by)
   VALUES ($1, ARRAY[$1 || '/current']::text[], NOW() - interval '1 hour', NOW() - interval '1 hour', NOW(), $2)
   RETURNING id`, path, workerID).Scan(&candidate.id)
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, candidate)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, paths)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, paths)
	})
	// These changes occur after claiming and the optimistic reference pre-check.
	for _, path := range []string{paths[0], paths[3]} {
		_, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, poster_path)
   VALUES ($1, 'movie', 'GC batch reference', 'matched', '{}'::text[], $1)`, path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.NewArtworkRevisionTracker(pool).TrackArtworkRevision(ctx, paths[1], "poster", []string{paths[1] + "/new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates SET deleted_at = NOW() WHERE original_path = $1`, paths[3]); err != nil {
		t.Fatal(err)
	}
	calls := 0
	deleter := artworkRevisionDeleteFunc(func(_ context.Context, keys []string) (int, error) {
		calls++
		want := []string{paths[2] + "/current", paths[3] + "/current"}
		if !slices.Equal(keys, want) {
			t.Errorf("deleted keys = %v, want current manifests %v", keys, want)
		}
		return len(keys), nil
	})
	pending, stats, err := NewArtworkRevisionGarbageCollector(pool, deleter).processCandidatesToHeal(ctx, candidates, workerID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || stats.Referenced != 1 || len(pending) != 2 {
		t.Fatalf("calls = %d, stats = %+v, pending = %v", calls, stats, pending)
	}
	for _, path := range paths[2:] {
		var tombstone bool
		if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM artwork_revision_gc_candidates WHERE original_path = $1`, path).Scan(&tombstone); err != nil {
			t.Fatal(err)
		}
		if !tombstone {
			t.Errorf("missing durable tombstone for %s", path)
		}
	}
	var nextAttempt *time.Time
	if err := pool.QueryRow(ctx, `SELECT next_attempt_at FROM artwork_revision_gc_candidates WHERE original_path = $1`, paths[0]).Scan(&nextAttempt); err != nil {
		t.Fatal(err)
	}
	if nextAttempt != nil {
		t.Errorf("referenced revision was not parked: %v", nextAttempt)
	}
}

func TestArtworkRevisionGCRunRetriesIncompleteBatch(t *testing.T) {
	for _, failBatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("error=%t", failBatch), func(t *testing.T) {
			pool := artworkRevisionGCTestPool(t)
			ctx := t.Context()
			paths := []string{"tmdb/movies/gc-batch-success/poster/original.old.webp", "gc-batch-failure"}
			for _, path := range paths {
				_, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates
     (original_path, object_keys, not_before, next_attempt_at)
     VALUES ($1, ARRAY[$1]::text[], NOW() - interval '1 hour', NOW() - interval '1 hour')`, path)
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, "gc-partial-publication")
				_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, paths)
			})
			calls := 0
			deleter := artworkRevisionDeleteFunc(func(ctx context.Context, keys []string) (int, error) {
				calls++
				if len(keys) == 2 {
					_, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
						VALUES ('gc-partial-publication', 'movie', 'GC partial publication', 'matched', '{}'::text[], $1, 'https://images.example/partial.jpg')`, paths[0])
					if err != nil {
						return 0, err
					}
					if failBatch {
						return 1, errors.New("injected batch error")
					}
					return 1, nil
				}
				if len(keys) == 1 && keys[0] == paths[0] {
					return 1, nil
				}
				return 0, nil
			})
			stats, err := NewArtworkRevisionGarbageCollector(pool, deleter).Run(ctx)
			if err == nil {
				t.Fatal("expected the partial batch error")
			}
			if calls != 1 || stats.Deleted != 0 || stats.Retried != 2 {
				t.Fatalf("calls = %d, stats = %+v, want one call and two durable retries", calls, stats)
			}
			var tombstone bool
			var attempts int
			var nextAttempt time.Time
			var lockedBy string
			if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL, attempt_count, next_attempt_at, locked_by
    FROM artwork_revision_gc_candidates WHERE original_path = $1`, paths[1]).Scan(&tombstone, &attempts, &nextAttempt, &lockedBy); err != nil {
				t.Fatal(err)
			}
			if !tombstone || attempts != 1 || !nextAttempt.After(time.Now()) || lockedBy != "" {
				t.Fatalf("failed candidate: tombstone=%t attempts=%d next=%v locked_by=%q", tombstone, attempts, nextAttempt, lockedBy)
			}
		})
	}
}

func TestArtworkRevisionGCRunHealsReferencePublishedDuringDelete(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := t.Context()
	const path = "tmdb/movies/gc-run-heal/poster/original.gone.webp"
	const contentID = "gc-run-heal"
	const source = "https://images.example/gc-run-heal.jpg"
	if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates
  (original_path, image_type, object_keys, not_before, next_attempt_at)
  VALUES ($1, 'poster', ARRAY[$1]::text[], NOW() - interval '1 hour', NOW() - interval '1 hour')`, path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, path)
	})
	deleter := artworkRevisionDeleteFunc(func(ctx context.Context, keys []string) (int, error) {
		_, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, poster_path, poster_source_path)
   VALUES ($1, 'movie', 'GC concurrent publication', 'matched', '{}'::text[], $2, $3)`, contentID, path, source)
		return len(keys), err
	})
	stats, err := NewArtworkRevisionGarbageCollector(pool, deleter).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Healed != 1 || stats.Deleted != 1 {
		t.Fatalf("stats = %+v, want one deletion and one healed reference", stats)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, contentID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != source {
		t.Fatalf("poster_path = %q, want remote source %q", got, source)
	}
}
