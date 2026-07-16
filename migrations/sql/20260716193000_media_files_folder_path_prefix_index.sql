-- +goose NO TRANSACTION

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_files_folder_file_path_pattern
    ON media_files (media_folder_id, file_path text_pattern_ops);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_media_files_folder_file_path_pattern;
