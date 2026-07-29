-- +goose NO TRANSACTION

-- +goose Up
-- Search evaluates these exact expressions in the episode candidate branch.
-- Build concurrently so large episode catalogs remain readable and writable.
-- Remove INVALID remnants first: IF NOT EXISTS otherwise accepts a failed
-- concurrent build and Goose would record a search index that cannot be used.
-- +goose StatementBegin
DO $$
DECLARE
    index_name text;
BEGIN
    FOREACH index_name IN ARRAY ARRAY[
        'idx_episodes_search_title',
        'idx_episodes_search_overview'
    ] LOOP
        IF EXISTS (
            SELECT 1
            FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            JOIN pg_index i ON i.indexrelid = c.oid
            WHERE n.nspname = 'public'
              AND c.relname = index_name
              AND NOT i.indisvalid
        ) THEN
            EXECUTE format('DROP INDEX public.%I', index_name);
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_search_title
ON public.episodes USING gin (
    setweight(
        to_tsvector(
            'simple',
            public.normalize_search_text(
                COALESCE(NULLIF(BTRIM(title), ''), 'Episode ' || episode_number::text)
            )
        ),
        'A'
    )
);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_search_overview
ON public.episodes USING gin (
    to_tsvector('english', COALESCE(overview, ''))
);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_search_overview;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_search_title;
