-- +goose Up
-- Fill the stream-node name snapshots that
-- 20261122163949_playback_analytics_node_snapshots_and_effective_media added,
-- for rows recorded before it. A separate migration, so the column changes
-- there commit (and release their table locks) before these scans run. Each
-- update only fills empty values, so running it again changes nothing.
-- +goose StatementBegin
UPDATE admin_playback_history h
SET routing_execution_node_name = NULLIF(BTRIM(sn.name), '')
FROM stream_nodes sn
WHERE h.routing_execution_node_id = sn.id
  AND h.routing_execution_node_name IS NULL;

UPDATE admin_playback_history h
SET routing_egress_node_name = NULLIF(BTRIM(sn.name), '')
FROM stream_nodes sn
WHERE h.routing_egress_node_id = sn.id
  AND h.routing_egress_node_name IS NULL;

UPDATE dashboard_node_egress_daily d
SET
    -- Fill each field only where it is missing: a recorded name stays even
    -- when only the type needs filling.
    node_name = CASE WHEN BTRIM(d.node_name) = '' THEN COALESCE(NULLIF(BTRIM(sn.name), ''), '') ELSE d.node_name END,
    node_type = COALESCE(d.node_type, sn.type)
FROM stream_nodes sn
WHERE d.node_id = sn.id
  AND (BTRIM(d.node_name) = '' OR d.node_type IS NULL);
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: the names stay as recorded snapshots.
