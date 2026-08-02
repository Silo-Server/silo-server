package worker

import "testing"

func TestCleanupRemovesWatermarkWithExpiredHeartbeat(t *testing.T) {
	pool := workerIntegrationPool(t)
	const node = "cleanup-watermark-test"
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO node_heartbeats (node_id, node_type, updated_at)
		VALUES ($1, 'api', NOW() - INTERVAL '10 minutes')
		ON CONFLICT (node_id) DO UPDATE SET updated_at=EXCLUDED.updated_at
	`, node); err != nil {
		t.Fatalf("seed stale heartbeat: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO playback_session_snapshot_watermarks
			(reporting_node, boot_generation, reconciliation_generation, completed_at, session_count)
		VALUES ($1, gen_random_uuid(), gen_random_uuid(), NOW(), 0)
		ON CONFLICT (reporting_node) DO UPDATE SET completed_at=EXCLUDED.completed_at
	`, node); err != nil {
		t.Fatalf("seed stale watermark: %v", err)
	}
	cleaner := NewSessionCleaner(pool, 0)
	if _, err := cleaner.CleanStale(t.Context()); err != nil {
		t.Fatalf("CleanStale: %v", err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM playback_session_snapshot_watermarks WHERE reporting_node=$1`, node).Scan(&count); err != nil {
		t.Fatalf("count watermark: %v", err)
	}
	if count != 0 {
		t.Fatalf("watermark count = %d, want 0", count)
	}
}

func TestCleanupRemovesPreExistingOrphanWatermark(t *testing.T) {
	pool := workerIntegrationPool(t)
	const node = "cleanup-orphan-watermark-test"
	if _, err := pool.Exec(t.Context(), `DELETE FROM node_heartbeats WHERE node_id=$1`, node); err != nil {
		t.Fatalf("clear heartbeat: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO playback_session_snapshot_watermarks
			(reporting_node, boot_generation, reconciliation_generation, completed_at, session_count)
		VALUES ($1, gen_random_uuid(), gen_random_uuid(), NOW(), 0)
		ON CONFLICT (reporting_node) DO UPDATE SET completed_at=EXCLUDED.completed_at
	`, node); err != nil {
		t.Fatalf("seed orphan watermark: %v", err)
	}

	cleaner := NewSessionCleaner(pool, 0)
	if _, err := cleaner.CleanStale(t.Context()); err != nil {
		t.Fatalf("CleanStale: %v", err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM playback_session_snapshot_watermarks WHERE reporting_node=$1`, node).Scan(&count); err != nil {
		t.Fatalf("count watermark: %v", err)
	}
	if count != 0 {
		t.Fatalf("orphan watermark count = %d, want 0", count)
	}
}

func TestCleanupRemovesOnlyExpiredSessionGenerationTombstones(t *testing.T) {
	pool := workerIntegrationPool(t)
	const sessionID = "cleanup-tombstone-test"
	if _, err := pool.Exec(t.Context(), `DELETE FROM playback_session_generation_tombstones WHERE session_id=$1`, sessionID); err != nil {
		t.Fatalf("clear tombstones: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO playback_session_generation_tombstones (session_id, session_generation, expires_at)
		VALUES ($1, gen_random_uuid(), NOW() - INTERVAL '1 second'),
		       ($1, gen_random_uuid(), NOW() + INTERVAL '1 hour')
	`, sessionID); err != nil {
		t.Fatalf("seed tombstones: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM playback_session_generation_tombstones WHERE session_id=$1`, sessionID)
	})

	cleaner := NewSessionCleaner(pool, 0)
	if _, err := cleaner.CleanStale(t.Context()); err != nil {
		t.Fatalf("CleanStale: %v", err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM playback_session_generation_tombstones WHERE session_id=$1`, sessionID).Scan(&count); err != nil {
		t.Fatalf("count tombstones: %v", err)
	}
	if count != 1 {
		t.Fatalf("remaining tombstones = %d, want 1 unexpired row", count)
	}
}
