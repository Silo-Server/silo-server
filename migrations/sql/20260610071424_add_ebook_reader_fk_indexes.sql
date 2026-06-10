-- +goose Up
-- +goose StatementBegin
-- The ebook reader tables carry ON DELETE CASCADE foreign keys on content_id
-- (and file_id for progress), but every existing index leads with user_id, so
-- media_items/media_files deletes had to seq-scan them. Plain btree indexes on
-- the FK columns let those cascades use index scans.
CREATE INDEX IF NOT EXISTS ebook_reader_progress_content_id
    ON ebook_reader_progress (content_id);

CREATE INDEX IF NOT EXISTS ebook_reader_progress_file_id
    ON ebook_reader_progress (file_id);

CREATE INDEX IF NOT EXISTS ebook_reader_config_content_id
    ON ebook_reader_config (content_id);

CREATE INDEX IF NOT EXISTS ebook_reader_annotations_content_id
    ON ebook_reader_annotations (content_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS ebook_reader_annotations_content_id;
DROP INDEX IF EXISTS ebook_reader_config_content_id;
DROP INDEX IF EXISTS ebook_reader_progress_file_id;
DROP INDEX IF EXISTS ebook_reader_progress_content_id;
-- +goose StatementEnd
