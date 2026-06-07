-- +goose Up
-- +goose StatementBegin
ALTER TABLE playback_sessions_sync
    ADD COLUMN IF NOT EXISTS requested_media_file_id integer;

UPDATE playback_sessions_sync
SET requested_media_file_id = media_file_id
WHERE requested_media_file_id IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE playback_sessions_sync
    DROP COLUMN IF EXISTS requested_media_file_id;
-- +goose StatementEnd
