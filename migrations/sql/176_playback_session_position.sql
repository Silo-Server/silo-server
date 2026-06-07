-- +goose Up
-- +goose StatementBegin
ALTER TABLE playback_sessions_sync
    ADD COLUMN IF NOT EXISTS position_seconds DOUBLE PRECISION NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE playback_sessions_sync
    DROP COLUMN IF EXISTS position_seconds;
-- +goose StatementEnd
