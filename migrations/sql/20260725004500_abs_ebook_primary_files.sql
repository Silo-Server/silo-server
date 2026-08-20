-- Persist the primary ebook selected through the Audiobookshelf compatibility
-- API. A missing row intentionally means the ABS default: prefer EPUB, then
-- the first supported ebook file. A row whose file_id is NULL is the explicit
-- "every file is supplementary" state ABS's status toggle produces, which is
-- why file_id is nullable rather than NOT NULL.
--
-- Both foreign keys CASCADE: a selection outlives neither its item nor its
-- pinned file. Deleting the pinned file removes the row, so the item falls
-- back to the EPUB-first default rather than reading as the explicit
-- no-primary state. The reader query additionally re-checks that the pinned
-- file still belongs to the item, which covers a file reassigned rather than
-- deleted.
-- +goose Up
CREATE TABLE IF NOT EXISTS abs_ebook_primary_files (
    content_id TEXT PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    file_id INTEGER REFERENCES media_files(id) ON DELETE CASCADE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS abs_ebook_primary_files;
