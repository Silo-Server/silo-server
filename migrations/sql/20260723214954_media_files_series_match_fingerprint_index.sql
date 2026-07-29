-- +goose NO TRANSACTION

-- +goose Up
-- The series queue fingerprint aggregates active paths under each observed
-- root. Build its supporting index without blocking scanner writes to the
-- potentially large media_files table during deployment.
-- A failed concurrent build can leave an INVALID relation that makes
-- IF NOT EXISTS skip every retry. Remove only that unusable artifact.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_media_files_folder_root_active'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_media_files_folder_root_active;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_files_folder_root_active
    ON public.media_files (media_folder_id, observed_root_path)
    WHERE missing_since IS NULL AND extra_id IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_media_files_folder_root_active;
