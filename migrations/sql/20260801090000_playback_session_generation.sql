-- +goose Up
-- +goose StatementBegin
ALTER TABLE playback_sessions_sync ADD COLUMN IF NOT EXISTS session_generation UUID NOT NULL DEFAULT gen_random_uuid();
CREATE INDEX IF NOT EXISTS playback_sessions_sync_generation_idx ON playback_sessions_sync (session_id, session_generation);

ALTER TABLE node_heartbeats
  ADD COLUMN IF NOT EXISTS boot_generation UUID NOT NULL DEFAULT gen_random_uuid();

CREATE TABLE IF NOT EXISTS playback_session_snapshot_watermarks (
  reporting_node TEXT PRIMARY KEY,
  boot_generation UUID NOT NULL,
  reconciliation_generation UUID NOT NULL,
  completed_at TIMESTAMPTZ NOT NULL,
  session_count INTEGER NOT NULL CHECK (session_count >= 0)
);

CREATE TABLE IF NOT EXISTS playback_session_generation_tombstones (
  session_id TEXT NOT NULL,
  session_generation UUID NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (session_id, session_generation)
);
CREATE INDEX IF NOT EXISTS playback_session_generation_tombstones_expiry_idx
  ON playback_session_generation_tombstones (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS playback_session_generation_tombstones;
DROP TABLE IF EXISTS playback_session_snapshot_watermarks;
ALTER TABLE node_heartbeats DROP COLUMN IF EXISTS boot_generation;
DROP INDEX IF EXISTS playback_sessions_sync_generation_idx;
ALTER TABLE playback_sessions_sync DROP COLUMN IF EXISTS session_generation;
-- +goose StatementEnd
