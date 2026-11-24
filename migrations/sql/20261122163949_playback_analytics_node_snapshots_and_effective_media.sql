-- +goose Up
-- Persist the actual played edition and audio track index on finalized
-- playback history (so auto-switched alternates and non-default audio tracks
-- are attributed accurately), and snapshot stream-node names/types on both
-- admin_playback_history and dashboard_node_egress_daily so deleted nodes and
-- cross-server restores never misattribute historical traffic to an unrelated
-- node that happens to share an integer ID. Filling the names for rows recorded
-- before this migration runs in 20261124162317, so this one holds its table
-- locks only for the metadata changes.
-- +goose StatementBegin
ALTER TABLE admin_playback_history
    ADD COLUMN IF NOT EXISTS effective_media_file_id INTEGER,
    ADD COLUMN IF NOT EXISTS audio_track_index INTEGER,
    ADD COLUMN IF NOT EXISTS routing_execution_node_name TEXT,
    ADD COLUMN IF NOT EXISTS routing_egress_node_name TEXT;

ALTER TABLE dashboard_node_egress_daily
    ADD COLUMN IF NOT EXISTS node_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS node_type TEXT;
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
