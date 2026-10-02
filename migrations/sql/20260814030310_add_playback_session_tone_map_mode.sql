-- +goose Up
ALTER TABLE playback_sessions_sync
    ADD COLUMN tone_map_mode TEXT;

-- +goose Down
ALTER TABLE playback_sessions_sync
    DROP COLUMN tone_map_mode;
