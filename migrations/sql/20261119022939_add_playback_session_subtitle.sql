-- +goose Up
-- The subtitle a session's current plan delivers (playback.SessionSubtitle as
-- JSON), so the admin views report it from any replica and after a restart;
-- NULL when the plan shows none. History keeps the one the play ended on.
ALTER TABLE playback_sessions_sync ADD COLUMN subtitle JSONB;
ALTER TABLE admin_playback_history ADD COLUMN subtitle JSONB;

-- +goose Down
ALTER TABLE admin_playback_history DROP COLUMN subtitle;
ALTER TABLE playback_sessions_sync DROP COLUMN subtitle;
