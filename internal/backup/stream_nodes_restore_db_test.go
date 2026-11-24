package backup

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
)

// TestCarryCredentialsReconcilesRestoredStreamNodesDB verifies that restoring a
// backup onto a server whose stream_nodes reuse integer IDs for different nodes
// snapshots historical node names, remaps non-matching archived egress rows to
// negative IDs, clears non-matching history FK IDs, and preserves matching nodes.
func TestCarryCredentialsReconcilesRestoredStreamNodesDB(t *testing.T) {
	live, liveURL := backuptest.Database(t)
	restored, restoredURL := backuptest.Database(t)
	ctx := t.Context()

	var userID int
	if err := live.QueryRow(ctx, `INSERT INTO users (role, username) VALUES ('admin', 'admin') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	dbExec(t, restored, `INSERT INTO users (id, role, username) VALUES ($1, 'admin', 'admin')`, userID)

	// Destination server has a different node at ID 1, and the same node at ID 2.
	dbExec(t, live, `
		INSERT INTO stream_nodes (id, name, type, url, enabled, healthy)
		VALUES (1, 'dst-edge', 'proxy', 'http://dst-edge:8090', TRUE, TRUE),
		       (2, 'shared-tc', 'transcode', 'http://shared-tc:8091', TRUE, TRUE)`)
	dbExec(t, restored, `
		INSERT INTO stream_nodes (id, name, type, url, enabled, healthy)
		VALUES (1, 'src-edge', 'proxy', 'http://src-edge:8090', TRUE, TRUE),
		       (2, 'shared-tc', 'transcode', 'http://shared-tc:8091', TRUE, TRUE)`)

	dbExec(t, restored, `
		INSERT INTO dashboard_node_egress_daily (day, node_id, node_name, node_type, egress_bytes, peak_egress_kbps)
		VALUES ('2026-10-01', 1, '', NULL, 500000, 12000),
		       ('2026-10-01', 2, 'shared-tc', 'transcode', 300000, 8000)`)
	dbExec(t, restored, `
		INSERT INTO dashboard_node_egress_samples (bucket, node_id, egress_kbps)
		VALUES ('2026-10-02T12:00:00Z', 1, 4000),
		       ('2026-10-02T12:00:00Z', 2, 2000)`)
	dbExec(t, restored, `
		INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at,
			 routing_execution, routing_execution_node_id, routing_egress, routing_egress_node_id, route_facts_recorded)
		VALUES ('sess-restore-1', $1, 'p-1', 'movie-1', 0, 'transcode', '2026-10-01T10:00:00Z', '2026-10-01T11:00:00Z',
			 'transcode', 2, 'proxy', 1, TRUE)`, userID)

	if _, err := (PGLoader{}).CarryCredentials(ctx, restoredURL, liveURL); err != nil {
		t.Fatalf("CarryCredentials: %v", err)
	}

	assertRestoredNodeReconciliation(t, restored)
}

func assertRestoredNodeReconciliation(t *testing.T, restored *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()

	var remappedID int
	var remappedName, remappedType string
	var remappedBytes int64
	if err := restored.QueryRow(ctx, `
		SELECT node_id, node_name, COALESCE(node_type, ''), egress_bytes
		FROM dashboard_node_egress_daily
		WHERE day = '2026-10-01' AND node_id < 0
	`).Scan(&remappedID, &remappedName, &remappedType, &remappedBytes); err != nil {
		t.Fatalf("expected remapped negative node row for src-edge: %v", err)
	}
	if remappedID >= 0 || remappedName != "src-edge" || remappedType != "proxy" || remappedBytes != 500000 {
		t.Fatalf("remapped daily row = id=%d name=%q type=%q bytes=%d", remappedID, remappedName, remappedType, remappedBytes)
	}

	// Unarchived same-day samples for unmatched src-edge are folded into the
	// daily archive under the same synthetic negative node_id rather than lost.
	var foldedID int
	var foldedName, foldedType string
	var foldedBytes, foldedPeak int64
	if err := restored.QueryRow(ctx, `
		SELECT node_id, node_name, COALESCE(node_type, ''), egress_bytes, peak_egress_kbps
		FROM dashboard_node_egress_daily
		WHERE day = '2026-10-02' AND node_id < 0
	`).Scan(&foldedID, &foldedName, &foldedType, &foldedBytes, &foldedPeak); err != nil {
		t.Fatalf("expected folded same-day daily row for src-edge: %v", err)
	}
	if foldedID != remappedID || foldedName != "src-edge" || foldedType != "proxy" ||
		foldedBytes != 4000*7500 || foldedPeak != 4000 {
		t.Fatalf("folded same-day row = id=%d (want %d) name=%q type=%q bytes=%d peak=%d",
			foldedID, remappedID, foldedName, foldedType, foldedBytes, foldedPeak)
	}

	var dstNode1Rows int
	if err := restored.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard_node_egress_daily WHERE node_id = 1`).Scan(&dstNode1Rows); err != nil || dstNode1Rows != 0 {
		t.Fatalf("daily rows still bound to destination node 1 = %d (err %v), want 0", dstNode1Rows, err)
	}

	var keptNode2Name string
	if err := restored.QueryRow(ctx, `SELECT node_name FROM dashboard_node_egress_daily WHERE day = '2026-10-01' AND node_id = 2`).Scan(&keptNode2Name); err != nil || keptNode2Name != "shared-tc" {
		t.Fatalf("matching node 2 daily row = %q (err %v), want shared-tc", keptNode2Name, err)
	}

	var sampleNode1, sampleNode2 int
	if err := restored.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE node_id = 1), COUNT(*) FILTER (WHERE node_id = 2) FROM dashboard_node_egress_samples`).Scan(&sampleNode1, &sampleNode2); err != nil {
		t.Fatal(err)
	}
	if sampleNode1 != 0 || sampleNode2 != 1 {
		t.Fatalf("restored node egress samples = node1:%d node2:%d, want 0 and 1", sampleNode1, sampleNode2)
	}

	var execID, egressID *int
	var execName, egressName string
	if err := restored.QueryRow(ctx, `
		SELECT routing_execution_node_id, COALESCE(routing_execution_node_name, ''),
		       routing_egress_node_id, COALESCE(routing_egress_node_name, '')
		FROM admin_playback_history WHERE session_id = 'sess-restore-1'
	`).Scan(&execID, &execName, &egressID, &egressName); err != nil {
		t.Fatal(err)
	}
	if execID == nil || *execID != 2 || execName != "shared-tc" || egressID != nil || egressName != "src-edge" {
		t.Fatalf("history node reconciliation = exec=%v/%q egress=%v/%q, want 2/shared-tc and nil/src-edge",
			execID, execName, egressID, egressName)
	}
}

// TestCarryCredentialsKeepsNodeSamplesWithoutDailyArchiveDB pins that a backup
// taken before dashboard_node_egress_daily existed keeps an unmatched node's
// raw samples (re-keyed off the live node's id) instead of deleting them, since
// there is no archive to fold them into.
func TestCarryCredentialsKeepsNodeSamplesWithoutDailyArchiveDB(t *testing.T) {
	live, liveURL := backuptest.Database(t)
	restored, restoredURL := backuptest.Database(t)
	ctx := t.Context()

	dbExec(t, live, `
		INSERT INTO stream_nodes (id, name, type, url, enabled, healthy)
		VALUES (1, 'dst-edge', 'proxy', 'http://dst-edge:8090', TRUE, TRUE),
		       (2, 'shared-tc', 'transcode', 'http://shared-tc:8091', TRUE, TRUE)`)
	dbExec(t, restored, `
		INSERT INTO stream_nodes (id, name, type, url, enabled, healthy)
		VALUES (1, 'src-edge', 'proxy', 'http://src-edge:8090', TRUE, TRUE),
		       (2, 'shared-tc', 'transcode', 'http://shared-tc:8091', TRUE, TRUE)`)
	dbExec(t, restored, `DROP TABLE dashboard_node_egress_daily`)
	dbExec(t, restored, `
		INSERT INTO dashboard_node_egress_samples (bucket, node_id, egress_kbps)
		VALUES ('2026-10-02T12:00:00Z', 1, 4000),
		       ('2026-10-02T12:00:00Z', 2, 2000)`)

	if _, err := (PGLoader{}).CarryCredentials(ctx, restoredURL, liveURL); err != nil {
		t.Fatalf("CarryCredentials: %v", err)
	}

	var onLiveNode1, matched, detached int
	if err := restored.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE node_id = 1), COUNT(*) FILTER (WHERE node_id = 2),
		       COUNT(*) FILTER (WHERE node_id < 0 AND egress_kbps = 4000)
		FROM dashboard_node_egress_samples`).Scan(&onLiveNode1, &matched, &detached); err != nil {
		t.Fatal(err)
	}
	if onLiveNode1 != 0 || matched != 1 || detached != 1 {
		t.Fatalf("samples after restore: on live node 1=%d, matched node 2=%d, detached src-edge=%d; want 0, 1, 1",
			onLiveNode1, matched, detached)
	}
}
