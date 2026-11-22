-- +goose Up
-- Persist the actual played edition and audio track index on finalized
-- playback history (so auto-switched alternates and non-default audio tracks
-- are attributed accurately), and snapshot stream-node names/types on both
-- admin_playback_history and dashboard_node_egress_daily so deleted nodes and
-- cross-server restores never misattribute historical traffic to an unrelated
-- node that happens to share an integer ID.
-- +goose StatementBegin
ALTER TABLE admin_playback_history
    ADD COLUMN IF NOT EXISTS effective_media_file_id INTEGER,
    ADD COLUMN IF NOT EXISTS audio_track_index INTEGER,
    ADD COLUMN IF NOT EXISTS routing_execution_node_name TEXT,
    ADD COLUMN IF NOT EXISTS routing_egress_node_name TEXT;

ALTER TABLE dashboard_node_egress_daily
    ADD COLUMN IF NOT EXISTS node_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS node_type TEXT;

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
    node_name = COALESCE(NULLIF(BTRIM(sn.name), ''), d.node_name),
    node_type = COALESCE(sn.type, d.node_type)
FROM stream_nodes sn
WHERE d.node_id = sn.id
  AND (d.node_name = '' OR d.node_type IS NULL);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE dashboard_node_egress_daily
    DROP COLUMN IF EXISTS node_type,
    DROP COLUMN IF EXISTS node_name;

ALTER TABLE admin_playback_history
    DROP COLUMN IF EXISTS routing_egress_node_name,
    DROP COLUMN IF EXISTS routing_execution_node_name,
    DROP COLUMN IF EXISTS audio_track_index,
    DROP COLUMN IF EXISTS effective_media_file_id;
-- +goose StatementEnd
