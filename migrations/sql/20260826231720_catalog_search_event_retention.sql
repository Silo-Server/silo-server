-- +goose NO TRANSACTION

-- +goose Up
-- Keep the sync hot-path index proportional to pending work. The original
-- index included processed_at as a key and therefore retained every completed
-- event even though pending queries always filter processed_at IS NULL.
-- A failed concurrent build can leave an invalid index that makes IF NOT
-- EXISTS skip the retry. Remove only that unusable artifact first.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_catalog_search_index_events_ready'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_catalog_search_index_events_ready;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_catalog_search_index_events_ready
    ON public.catalog_search_index_events (provider, available_at, id)
    WHERE processed_at IS NULL;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_catalog_search_index_events_pending;

-- +goose Down
-- Apply the same crash-retry guard when restoring the original index.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_catalog_search_index_events_pending'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_catalog_search_index_events_pending;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_catalog_search_index_events_pending
    ON public.catalog_search_index_events (provider, processed_at, available_at, id);

DROP INDEX CONCURRENTLY IF EXISTS public.idx_catalog_search_index_events_ready;
