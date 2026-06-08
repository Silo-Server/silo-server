-- +goose Up
-- +goose StatementBegin
ALTER TABLE playback_sessions_sync
    ADD COLUMN IF NOT EXISTS transcode_hw_accel text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE playback_sessions_sync
    DROP COLUMN IF EXISTS transcode_hw_accel;
-- +goose StatementEnd
