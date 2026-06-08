-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_files
    ADD COLUMN IF NOT EXISTS match_suppressed_at timestamp with time zone;

CREATE INDEX IF NOT EXISTS idx_media_files_unmatched_match_ready
    ON media_files (media_folder_id, match_suppressed_at, match_attempted_at ASC NULLS FIRST, id ASC)
    WHERE (content_id IS NULL OR content_id = '')
      AND missing_since IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_media_files_unmatched_match_ready;

ALTER TABLE media_files
    DROP COLUMN IF EXISTS match_suppressed_at;
-- +goose StatementEnd
