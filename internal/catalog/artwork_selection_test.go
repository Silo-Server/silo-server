package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTrackArtworkRevisionSQLPreservesSeedAdoptionGrace(t *testing.T) {
	for _, want := range []string{
		"source_class = 'seed'",
		"COALESCE(artwork_revision_gc_candidates.seed_expires_at, artwork_revision_gc_candidates.not_before)",
		"COALESCE(artwork_revision_gc_candidates.seed_expires_at, artwork_revision_gc_candidates.next_attempt_at)",
		"GREATEST(",
	} {
		if !strings.Contains(trackArtworkRevisionSQL, want) {
			t.Fatalf("tracking SQL does not preserve seed grace %q", want)
		}
	}
}

func TestArtworkRevisionTrackerReadsStoreBindingLive(t *testing.T) {
	binding := "local:generation-1"
	tracker := &ArtworkRevisionTracker{storeBinding: func() string { return binding }}
	if got := tracker.currentStoreBinding(); got != binding {
		t.Fatalf("store binding = %q, want %q", got, binding)
	}
	binding = "local:generation-2"
	if got := tracker.currentStoreBinding(); got != binding {
		t.Fatalf("rotated store binding = %q, want %q", got, binding)
	}
}

func TestRetainUntrackedArtworkRevisionSQLDisarmsAnyCandidate(t *testing.T) {
	for _, want := range []string{
		"source_class = 'seed'",
		"seed_imported_at = COALESCE(seed_imported_at, NOW())",
		"seed_expires_at = NULL",
		"next_attempt_at = NULL",
		"tombstoned_at = NULL",
		"WHERE original_path = $1",
	} {
		if !strings.Contains(retainUntrackedArtworkRevisionSQL, want) {
			t.Fatalf("retention SQL is missing %q", want)
		}
	}
	if strings.Contains(retainUntrackedArtworkRevisionSQL, "WHERE original_path = $1 AND source_class = 'seed'") {
		t.Fatal("retention remains restricted to imported seeds")
	}
	if !strings.Contains(trackArtworkRevisionSQL, "WHEN artwork_revision_gc_candidates.next_attempt_at IS NULL THEN NULL") {
		t.Fatal("tracking can re-arm a retained untracked revision")
	}
}

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
	tracker := NewArtworkRevisionTracker(pool)
	if err := tracker.ParkArtworkRevision(ctx, path, "poster"); err != nil {
		t.Fatalf("ParkArtworkRevision: %v", err)
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

func TestRetainedUntrackedArtworkRevisionSurvivesDisplacementRearm(t *testing.T) {
	pool := newArtworkSelectionTestPool(t)
	ctx := context.Background()
	path := fmt.Sprintf("artwork/v1/objects/collection_poster/%d/original.webp", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, path)
	})
	if err := queueArtworkRevisionGC(ctx, pool, path, "collection_poster", time.Now()); err != nil {
		t.Fatalf("seed existing candidate: %v", err)
	}

	tracker := NewArtworkRevisionTracker(pool)
	if err := tracker.RetainUntrackedArtworkRevision(ctx, path); err != nil {
		t.Fatalf("retain untracked revision: %v", err)
	}
	if err := queueArtworkRevisionGC(ctx, pool, path, "collection_poster", time.Now()); err != nil {
		t.Fatalf("simulate catalog displacement re-arm: %v", err)
	}

	var sourceClass string
	var importedAt *time.Time
	var expiresAt, nextAttempt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT source_class, seed_imported_at, seed_expires_at, next_attempt_at
		FROM artwork_revision_gc_candidates WHERE original_path = $1`, path).Scan(
		&sourceClass, &importedAt, &expiresAt, &nextAttempt,
	); err != nil {
		t.Fatalf("load retained candidate: %v", err)
	}
	if sourceClass != "seed" || importedAt == nil || expiresAt != nil || nextAttempt != nil {
		t.Fatalf("retained candidate = source:%q imported:%v expires:%v next:%v", sourceClass, importedAt, expiresAt, nextAttempt)
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
