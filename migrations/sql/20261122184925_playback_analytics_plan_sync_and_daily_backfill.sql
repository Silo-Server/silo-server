-- +goose Up

ALTER TABLE playback_sessions_sync
    ADD COLUMN IF NOT EXISTS plan_id TEXT;

-- The daily archive backfill runs in 20261124162320, so this migration holds
-- the playback_sessions_sync lock only for the column change.

-- +goose Down

ALTER TABLE playback_sessions_sync
    DROP COLUMN IF EXISTS plan_id;
