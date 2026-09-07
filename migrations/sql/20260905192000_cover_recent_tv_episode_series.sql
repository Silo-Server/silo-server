-- +goose NO TRANSACTION

-- +goose Up
-- TV availability grouping needs only the episode ID and its parent series.
-- Cover those reads without fetching metadata from every matching episode.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_index i
        JOIN pg_class c ON c.oid = i.indexrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_episodes_content_series'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_episodes_content_series;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_content_series
    ON public.episodes (content_id) INCLUDE (series_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_content_series;
