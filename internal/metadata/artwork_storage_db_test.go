package metadata

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

// A rebuild must not be able to rotate the store durably while the accounting
// row still says healthy: that combination leaves an empty store that nothing
// ever re-enters recovery for. Proving the ordering only needs a rotation that
// fails — the intent has to be durable regardless.
func TestRebuildEmptyRecordsIntentBeforeRotating(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := t.Context()
	resetArtworkAccountingState(t, pool)

	service := &ArtworkStorageService{pool: pool, backend: artworkstore.BackendLocal, rebuilder: &artworkstore.Handle{}}
	if _, err := service.RebuildEmpty(ctx); !errors.Is(err, artworkstore.ErrRebuildUnsupported) {
		t.Fatalf("RebuildEmpty error = %v, want ErrRebuildUnsupported", err)
	}
	var health string
	if err := pool.QueryRow(ctx, `SELECT store_health FROM artwork_storage_accounting_state WHERE singleton`).Scan(&health); err != nil {
		t.Fatalf("read store health: %v", err)
	}
	if health != string(artworkstore.HealthEmptyRebuilding) {
		t.Fatalf("store health after a failed rotation = %q, want empty_rebuilding", health)
	}
}

func TestRebuildEmptyPersistsRotatedGeneration(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := t.Context()
	resetArtworkAccountingState(t, pool)

	handle, err := artworkstore.Open(ctx, artworkstore.Options{
		Backend: artworkstore.BackendLocal, LocalPath: t.TempDir(), Settings: alertTestSettings{},
	})
	if err != nil {
		t.Fatalf("open rebuild test store: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	service := &ArtworkStorageService{
		pool: pool, store: handle.Store, backend: artworkstore.BackendLocal,
		generation: handle.GenerationID, rebuilder: handle,
	}
	before := handle.GenerationID()
	if _, err := service.RebuildEmpty(ctx); err != nil {
		t.Fatalf("RebuildEmpty: %v", err)
	}
	var health, generation string
	if err := pool.QueryRow(ctx, `SELECT store_health, rebuild_generation
		FROM artwork_storage_accounting_state WHERE singleton`).Scan(&health, &generation); err != nil {
		t.Fatalf("read rebuild state: %v", err)
	}
	// The observable end state of a successful rebuild is unchanged: the
	// accounting row names the live generation and the store is rebuilding.
	if health != string(artworkstore.HealthEmptyRebuilding) {
		t.Fatalf("store health = %q, want empty_rebuilding", health)
	}
	if live := handle.GenerationID(); generation != live {
		t.Fatalf("persisted generation = %q, want the live %q (was %q)", generation, live, before)
	}
	if !shouldReenterArtworkRecovery(artworkRecoveryState{storeHealth: health, rebuildGeneration: generation}, artworkstore.HealthHealthy) {
		t.Fatal("a persisted rebuild must remain re-enterable after a restart")
	}
}

// A resumed refresh restores its pre-interruption counters and never revisits
// the revisions behind its cursor. The published snapshot must therefore read
// missing objects out of the registry, not out of the restored counter.
func TestRefreshResultRecomputesMissingObjectsFromRegistry(t *testing.T) {
	pool := localArtworkTestPool(t)
	ctx := t.Context()
	// A run-unique store generation keeps the recomputed count scoped to this
	// test's rows in the shared test database.
	run := time.Now().UnixNano()
	service := &ArtworkStorageService{pool: pool, backend: artworkstore.BackendLocal,
		generation: func() string { return fmt.Sprintf("refresh-recompute-%d", run) }}
	generation := service.storeGeneration()

	path := fmt.Sprintf("artwork/v1/refresh-recompute-%d/poster/original.webp", run)
	t.Cleanup(func() {
		// t.Context is already canceled by the time cleanup runs.
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE store_generation = $1`, generation)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO artwork_revision_gc_candidates
			(original_path, image_type, object_keys, object_sizes_bytes, object_content_types,
			 total_physical_bytes, source_class, store_generation, inventory_complete, not_before)
		VALUES ($1, 'poster', ARRAY[$1, $1 || '.w300'], ARRAY[0, 0]::bigint[], ARRAY['', '']::text[],
			0, 'provider', $2, FALSE, NOW())`, path, generation); err != nil {
		t.Fatalf("seed incomplete revision: %v", err)
	}

	// The checkpoint claims far more missing objects than the registry holds:
	// the stale value must not reach the snapshot.
	stale := ArtworkInventoryCheckpoint{Version: artworkInventoryCheckpointVersion, MissingObjects: 99, KnownRevisions: 7}
	result, err := service.refreshResult(ctx, stale)
	if err != nil {
		t.Fatalf("refreshResult: %v", err)
	}
	if result.MissingObjects != 2 {
		t.Fatalf("missing objects = %d, want the 2 absent object slots in the registry", result.MissingObjects)
	}
	if result.Complete {
		t.Fatal("a registry with absent objects must not report a complete inventory")
	}

	// Repairing the revision clears the drift without a from-scratch refresh.
	if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates
		SET object_sizes_bytes = ARRAY[10, 20]::bigint[], object_content_types = ARRAY['image/webp', 'image/webp']::text[],
			total_physical_bytes = 30, inventory_complete = TRUE
		WHERE original_path = $1`, path); err != nil {
		t.Fatalf("repair revision: %v", err)
	}
	repaired, err := service.refreshResult(ctx, stale)
	if err != nil {
		t.Fatalf("refreshResult after repair: %v", err)
	}
	if repaired.MissingObjects != 0 {
		t.Fatalf("missing objects after repair = %d, want 0", repaired.MissingObjects)
	}
	if repaired.Failures != stale.Failures {
		t.Fatalf("failures = %d, want the run's own observation %d", repaired.Failures, stale.Failures)
	}
}

// resetArtworkAccountingState restores the singleton row to a healthy baseline
// so a rebuild test observes only its own writes.
func resetArtworkAccountingState(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	restore := func() {
		// context.Background, not t.Context: cleanup runs after it is canceled.
		_, _ = pool.Exec(context.Background(), `UPDATE artwork_storage_accounting_state SET
			store_health = 'healthy', rebuild_generation = '', rebuild_surface_name = '',
			rebuild_enqueued_at = NULL WHERE singleton`)
	}
	restore()
	t.Cleanup(restore)
}
