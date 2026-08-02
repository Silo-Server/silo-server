package worker

import (
	"testing"

	"github.com/google/uuid"
)

func TestNodeProducerConstructorsNormalizeNodeIDs(t *testing.T) {
	bootGeneration := uuid.New()
	hw := NewHeartbeatWriter(nil, "  api-node  ", "api", "", bootGeneration)
	if hw.nodeID != "api-node" {
		t.Fatalf("heartbeat node ID = %q, want normalized api-node", hw.nodeID)
	}
	r := NewReconciler(nil, "  api-node  ", nil, bootGeneration)
	if r.nodeName != "api-node" {
		t.Fatalf("reconciler node ID = %q, want normalized api-node", r.nodeName)
	}
}

func TestHeartbeatBeatWritesNormalizedNodeID(t *testing.T) {
	pool := workerIntegrationPool(t)
	const normalized = "heartbeat-normalized-test"
	if _, err := pool.Exec(t.Context(), `DELETE FROM node_heartbeats WHERE node_id IN ($1, $2)`, normalized, " "+normalized+" "); err != nil {
		t.Fatalf("clear heartbeat rows: %v", err)
	}
	hw := NewHeartbeatWriter(pool, " "+normalized+" ", "api", "", uuid.New())
	if err := hw.Beat(t.Context()); err != nil {
		t.Fatalf("Beat: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM node_heartbeats WHERE node_id=$1`, normalized)
	})
	var normalizedCount, paddedCount int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FILTER (WHERE node_id=$1), count(*) FILTER (WHERE node_id=$2)
		FROM node_heartbeats
	`, normalized, " "+normalized+" ").Scan(&normalizedCount, &paddedCount); err != nil {
		t.Fatalf("count heartbeat rows: %v", err)
	}
	if normalizedCount != 1 || paddedCount != 0 {
		t.Fatalf("heartbeat counts normalized=%d padded=%d, want 1 and 0", normalizedCount, paddedCount)
	}
}

func TestHeartbeatCleanupSelfRemovesSnapshotWatermark(t *testing.T) {
	pool := workerIntegrationPool(t)
	const node = "heartbeat-cleanup-test"
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO node_heartbeats (node_id, node_type, updated_at)
		VALUES ($1, 'api', NOW()) ON CONFLICT (node_id) DO UPDATE SET updated_at=EXCLUDED.updated_at
	`, node); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO playback_session_snapshot_watermarks
			(reporting_node, boot_generation, reconciliation_generation, completed_at, session_count)
		VALUES ($1, (SELECT boot_generation FROM node_heartbeats WHERE node_id=$1), gen_random_uuid(), NOW(), 0)
		ON CONFLICT (reporting_node) DO UPDATE SET completed_at=EXCLUDED.completed_at
	`, node); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
	hw := NewHeartbeatWriter(pool, node, "api", "", uuid.New())
	if err := hw.CleanupSelf(t.Context()); err != nil {
		t.Fatalf("CleanupSelf: %v", err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM playback_session_snapshot_watermarks WHERE reporting_node=$1`, node).Scan(&count); err != nil {
		t.Fatalf("count watermark: %v", err)
	}
	if count != 0 {
		t.Fatalf("watermark count = %d, want 0", count)
	}
}
