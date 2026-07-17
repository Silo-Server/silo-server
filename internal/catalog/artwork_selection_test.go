package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newArtworkSelectionTestPool(t *testing.T) *pgxpool.Pool {
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

func TestQueueAndParkArtworkRevisionUpserts(t *testing.T) {
	pool := newArtworkSelectionTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	path := fmt.Sprintf("tmdb/movies/%d/poster/original.rev.webp", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, path)
	})

	// Arming registers the revision for verification after the grace period.
	if err := queueArtworkRevisionGC(ctx, pool, path, "poster", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("queueArtworkRevisionGC: %v", err)
	}
	var imageType string
	var nextAttempt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT image_type, next_attempt_at FROM artwork_revision_gc_candidates
		WHERE original_path = $1`, path).Scan(&imageType, &nextAttempt); err != nil {
		t.Fatalf("load armed candidate: %v", err)
	}
	if imageType != "poster" {
		t.Fatalf("image_type = %q, want poster", imageType)
	}
	if nextAttempt == nil {
		t.Fatal("armed candidate has NULL next_attempt_at")
	}

	// Publication parks the selected revision: referenced by construction.
	if err := parkArtworkRevision(ctx, pool, path, "poster", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("parkArtworkRevision: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at FROM artwork_revision_gc_candidates
		WHERE original_path = $1`, path).Scan(&nextAttempt); err != nil {
		t.Fatalf("load parked candidate: %v", err)
	}
	if nextAttempt != nil {
		t.Fatalf("parked candidate next_attempt_at = %v, want NULL", *nextAttempt)
	}
}

func TestTrackArtworkRevisionKeepsDormantRowsDormant(t *testing.T) {
	pool := newArtworkSelectionTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	path := fmt.Sprintf("tmdb/movies/%d/poster/original.live.webp", suffix)
	keys := []string{path, fmt.Sprintf("tmdb/movies/%d/poster/w500.live.webp", suffix)}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, path)
	})

	if err := parkArtworkRevision(ctx, pool, path, "poster", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("parkArtworkRevision: %v", err)
	}
	// A re-cache of live artwork must not re-arm the parked row.
	tracker := NewArtworkRevisionTracker(pool)
	if err := tracker.TrackArtworkRevision(ctx, path, "poster", keys); err != nil {
		t.Fatalf("TrackArtworkRevision: %v", err)
	}

	var nextAttempt *time.Time
	var storedKeys []string
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at, object_keys FROM artwork_revision_gc_candidates
		WHERE original_path = $1`, path).Scan(&nextAttempt, &storedKeys); err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	if nextAttempt != nil {
		t.Fatalf("re-cache re-armed dormant row: next_attempt_at = %v", *nextAttempt)
	}
	if !slices.Equal(storedKeys, keys) {
		t.Fatalf("stored manifest = %v, want exact tracked manifest %v", storedKeys, keys)
	}
}
