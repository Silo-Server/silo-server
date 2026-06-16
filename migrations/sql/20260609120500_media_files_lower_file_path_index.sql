-- +goose NO TRANSACTION

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_files_folder_lower_file_path
    ON media_files (media_folder_id, lower(file_path));

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_media_files_folder_lower_file_path;
