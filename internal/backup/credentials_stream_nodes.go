package backup

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/dashmetrics"
)

// reconcileRestoredStreamNodeReferences runs inside carryCredentials right
// before the backup's stream_nodes rows are replaced by the live server's
// stream_nodes. Any historical reference pointing to a backup node whose
// (id, url) does not match a live node is decoupled from live stream_nodes.id
// while preserving its snapshotted node name/type, so restoring a backup onto
// another server can never attribute or merge old traffic under an unrelated
// destination node that happens to share an integer ID.
func reconcileRestoredStreamNodeReferences(ctx context.Context, tx pgx.Tx, liveStreamNodesJSON string) error {
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE IF NOT EXISTS matched_stream_nodes (id integer PRIMARY KEY) ON COMMIT DROP`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE matched_stream_nodes`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO matched_stream_nodes (id)
		SELECT b.id
		FROM stream_nodes b
		JOIN json_populate_recordset(NULL::stream_nodes, $1::json) l
		  ON l.id = b.id AND l.url IS NOT DISTINCT FROM b.url
	`, liveStreamNodesJSON); err != nil {
		return err
	}
	if err := reconcileRestoredHistoryNodes(ctx, tx); err != nil {
		return err
	}
	if err := foldUnmatchedNodeEgressSamples(ctx, tx); err != nil {
		return err
	}
	return reconcileRestoredDailyNodeEgress(ctx, tx)
}

func reconcileRestoredHistoryNodes(ctx context.Context, tx pgx.Tx) error {
	ok, err := tableExists(ctx, tx, "admin_playback_history")
	if err != nil || !ok {
		return err
	}
	cols, err := tableColumns(ctx, tx, "admin_playback_history")
	if err != nil {
		return err
	}
	has := func(col string) bool { return slices.Contains(cols, col) }
	if !has("routing_execution_node_id") || !has("routing_egress_node_id") {
		return nil
	}
	if has("routing_execution_node_name") {
		if _, err := tx.Exec(ctx, `
			UPDATE admin_playback_history h
			SET routing_execution_node_name = NULLIF(BTRIM(b.name), '')
			FROM stream_nodes b
			WHERE h.routing_execution_node_id = b.id
			  AND (h.routing_execution_node_name IS NULL OR BTRIM(h.routing_execution_node_name) = '')
		`); err != nil {
			return err
		}
	}
	if has("routing_egress_node_name") {
		if _, err := tx.Exec(ctx, `
			UPDATE admin_playback_history h
			SET routing_egress_node_name = NULLIF(BTRIM(b.name), '')
			FROM stream_nodes b
			WHERE h.routing_egress_node_id = b.id
			  AND (h.routing_egress_node_name IS NULL OR BTRIM(h.routing_egress_node_name) = '')
		`); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE admin_playback_history
		SET
			routing_execution_node_id = CASE
				WHEN routing_execution_node_id IS NOT NULL
				 AND NOT EXISTS (SELECT 1 FROM matched_stream_nodes m WHERE m.id = routing_execution_node_id)
				THEN NULL ELSE routing_execution_node_id END,
			routing_egress_node_id = CASE
				WHEN routing_egress_node_id IS NOT NULL
				 AND NOT EXISTS (SELECT 1 FROM matched_stream_nodes m WHERE m.id = routing_egress_node_id)
				THEN NULL ELSE routing_egress_node_id END
		WHERE (routing_execution_node_id IS NOT NULL
		       AND NOT EXISTS (SELECT 1 FROM matched_stream_nodes m WHERE m.id = routing_execution_node_id))
		   OR (routing_egress_node_id IS NOT NULL
		       AND NOT EXISTS (SELECT 1 FROM matched_stream_nodes m WHERE m.id = routing_egress_node_id))
	`)
	return err
}

func reconcileRestoredDailyNodeEgress(ctx context.Context, tx pgx.Tx) error {
	ok, err := tableExists(ctx, tx, "dashboard_node_egress_daily")
	if err != nil || !ok {
		return err
	}
	cols, err := tableColumns(ctx, tx, "dashboard_node_egress_daily")
	if err != nil {
		return err
	}
	if slices.Contains(cols, "node_name") && slices.Contains(cols, "node_type") {
		if _, err := tx.Exec(ctx, `
			UPDATE dashboard_node_egress_daily d
			SET
				node_name = COALESCE(NULLIF(BTRIM(d.node_name), ''), NULLIF(BTRIM(b.name), ''), ''),
				node_type = COALESCE(d.node_type, b.type)
			FROM stream_nodes b
			WHERE d.node_id = b.id
			  AND (BTRIM(d.node_name) = '' OR d.node_type IS NULL)
		`); err != nil {
			return err
		}
	}
	return remapUnmatchedNodeIDs(ctx, tx, "dashboard_node_egress_daily")
}

// foldUnmatchedNodeEgressSamples folds raw per-node samples from unmatched
// backup nodes into dashboard_node_egress_daily before deleting them from the
// raw sample table. That preserves unarchived same-day node egress while
// letting reconcileRestoredDailyNodeEgress remap the old node's prior days and
// restore-day rows together under one synthetic negative node_id. A backup
// from before the daily archive has nowhere to fold them, so its samples stay
// in the raw table under synthetic negative ids instead, where they no longer
// count toward a live node that reuses the id.
func foldUnmatchedNodeEgressSamples(ctx context.Context, tx pgx.Tx) error {
	okSamples, err := tableExists(ctx, tx, "dashboard_node_egress_samples")
	if err != nil || !okSamples {
		return err
	}
	folded, err := insertFoldedUnmatchedNodeEgress(ctx, tx)
	if err != nil {
		return err
	}
	if !folded {
		return remapUnmatchedNodeIDs(ctx, tx, "dashboard_node_egress_samples")
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM dashboard_node_egress_samples s
		WHERE NOT EXISTS (SELECT 1 FROM matched_stream_nodes m WHERE m.id = s.node_id)
	`)
	return err
}

// insertFoldedUnmatchedNodeEgress archives unmatched nodes' raw samples into
// dashboard_node_egress_daily. folded is false when the backup's schema has no
// archive (or one without node names) to fold them into.
func insertFoldedUnmatchedNodeEgress(ctx context.Context, tx pgx.Tx) (folded bool, err error) {
	okDaily, err := tableExists(ctx, tx, "dashboard_node_egress_daily")
	if err != nil || !okDaily {
		return false, err
	}
	cols, err := tableColumns(ctx, tx, "dashboard_node_egress_daily")
	if err != nil {
		return false, err
	}
	if !slices.Contains(cols, "node_name") || !slices.Contains(cols, "node_type") {
		return false, nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO dashboard_node_egress_daily (day, node_id, node_name, node_type, egress_bytes, peak_egress_kbps, updated_at)
		SELECT
			(s.bucket AT TIME ZONE 'UTC')::date AS day,
			s.node_id,
			COALESCE(NULLIF(BTRIM(b.name), ''), '') AS node_name,
			b.type AS node_type,
			(COALESCE(SUM(s.egress_kbps), 0) * $1)::bigint AS egress_bytes,
			COALESCE(MAX(s.egress_kbps), 0)::bigint        AS peak_egress_kbps,
			now()
		FROM dashboard_node_egress_samples s
		LEFT JOIN stream_nodes b ON b.id = s.node_id
		WHERE NOT EXISTS (SELECT 1 FROM matched_stream_nodes m WHERE m.id = s.node_id)
		GROUP BY 1, 2, 3, 4
		ON CONFLICT (day, node_id) DO UPDATE SET
			node_name        = COALESCE(NULLIF(BTRIM(dashboard_node_egress_daily.node_name), ''), EXCLUDED.node_name),
			node_type        = COALESCE(dashboard_node_egress_daily.node_type, EXCLUDED.node_type),
			egress_bytes     = GREATEST(dashboard_node_egress_daily.egress_bytes, EXCLUDED.egress_bytes),
			peak_egress_kbps = GREATEST(dashboard_node_egress_daily.peak_egress_kbps, EXCLUDED.peak_egress_kbps),
			updated_at       = now()
	`, int64(dashmetrics.BytesPerKbpsMinute))
	return err == nil, err
}

// remapUnmatchedNodeIDs moves the rows of table (a node egress table keyed by
// node_id) that belong to a backup node with no live match to synthetic
// negative ids below any already in use, one per old node, so they stay
// readable without counting toward a live node that reuses the id.
func remapUnmatchedNodeIDs(ctx context.Context, tx pgx.Tx, table string) error {
	_, err := tx.Exec(ctx, fmt.Sprintf(`
		WITH base AS (
			SELECT LEAST(0, COALESCE(MIN(node_id), 0)) AS min_id
			FROM %[1]s
		),
		remap AS (
			SELECT
				d.node_id AS old_id,
				(SELECT min_id FROM base) - ROW_NUMBER() OVER (ORDER BY d.node_id)::integer AS new_id
			FROM (
				SELECT DISTINCT node_id
				FROM %[1]s
				WHERE node_id > 0
				  AND NOT EXISTS (SELECT 1 FROM matched_stream_nodes m WHERE m.id = node_id)
			) d
		)
		UPDATE %[1]s t
		SET node_id = r.new_id
		FROM remap r
		WHERE t.node_id = r.old_id
	`, table))
	return err
}
