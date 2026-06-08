-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_files
    ADD COLUMN IF NOT EXISTS file_modified_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_media_files_folder_file_modified_at
    ON media_files (media_folder_id, file_modified_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_media_files_folder_file_modified_at;

ALTER TABLE media_files
    DROP COLUMN IF EXISTS file_modified_at;
-- +goose StatementEnd
