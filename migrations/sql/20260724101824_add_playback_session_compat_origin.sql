-- +goose Up
ALTER TABLE playback_sessions_sync
    ADD COLUMN compat_origin boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE playback_sessions_sync
    DROP COLUMN compat_origin;
