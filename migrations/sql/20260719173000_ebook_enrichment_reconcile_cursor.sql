-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE IF NOT EXISTS ebook_enrichment_reconcile_cursors (
    folder_id integer PRIMARY KEY REFERENCES media_folders(id) ON DELETE CASCADE,
    after_first_seen_at timestamptz,
    after_content_id text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((after_first_seen_at IS NULL) = (after_content_id IS NULL))
);

-- A failed CREATE INDEX CONCURRENTLY can leave an invalid index that blocks an
-- IF NOT EXISTS retry. Drop only that unusable artifact before rebuilding.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_item_libraries_folder_enrichment_cursor'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_item_libraries_folder_enrichment_cursor;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_item_libraries_folder_enrichment_cursor
    ON public.media_item_libraries (media_folder_id, first_seen_at DESC, content_id DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_item_libraries_folder_enrichment_cursor;
DROP TABLE IF EXISTS ebook_enrichment_reconcile_cursors;
