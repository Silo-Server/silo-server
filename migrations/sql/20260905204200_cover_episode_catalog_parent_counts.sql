-- +goose NO TRANSACTION

-- +goose Up
-- Exact episode totals check each entry's parent type and snapshot cutoff.
-- Keep those reads in a narrow library-scoped index.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_index i
        JOIN pg_class c ON c.oid = i.indexrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_episode_catalog_entries_parent_count'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_episode_catalog_entries_parent_count;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episode_catalog_entries_parent_count
    ON public.episode_catalog_entries (media_folder_id, series_id, episode_created_at);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_parent_count;
