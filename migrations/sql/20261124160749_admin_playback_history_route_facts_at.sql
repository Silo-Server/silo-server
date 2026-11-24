-- +goose Up
-- route_facts_at is when a row's route facts were last known. A finalization
-- replaces the route facts only when its own are at least as recent, which
-- ended_at cannot tell: only a copy that knows more watch time moves ended_at.
-- NULL means the row's ended_at, which is what rows recorded before this
-- column compared by.
ALTER TABLE admin_playback_history ADD COLUMN IF NOT EXISTS route_facts_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE admin_playback_history DROP COLUMN IF EXISTS route_facts_at;
