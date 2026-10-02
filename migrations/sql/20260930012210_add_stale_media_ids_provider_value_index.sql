-- +goose NO TRANSACTION

-- +goose Up
-- Watchlisted titles resolve against the provider IDs a library item has
-- rejected as well as its current ones, so a title TMDB deleted as a duplicate
-- still finds the item that once carried it. The primary key leads with
-- content_id and cannot serve a lookup by value. Build concurrently: the
-- metadata refresh path writes this table constantly.
-- Remove an INVALID remnant first: IF NOT EXISTS otherwise accepts a failed
-- concurrent build and Goose would record an index that is never used.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_stale_media_ids_provider_value'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_stale_media_ids_provider_value;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_stale_media_ids_provider_value
ON public.stale_media_ids (provider, provider_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_stale_media_ids_provider_value;
