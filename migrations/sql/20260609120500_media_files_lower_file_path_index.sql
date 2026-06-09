-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_media_files_folder_lower_file_path
    ON media_files (media_folder_id, lower(file_path));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_media_files_folder_lower_file_path;
-- +goose StatementEnd
