-- +goose NO TRANSACTION
-- +goose Up
-- Keep provider failures out of the recurring person refresh batch without
-- changing updated_at, which continues to describe actual metadata changes.
ALTER TABLE public.people
    ADD COLUMN IF NOT EXISTS metadata_refresh_attempted_at timestamptz;

-- The worker orders and filters by this expression every ten minutes. Building
-- concurrently avoids blocking catalog writes on large people tables.
-- A failed concurrent build can leave an invalid index that blocks an
-- IF NOT EXISTS retry, so discard only that unusable artifact first.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_people_metadata_refresh_due'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_people_metadata_refresh_due;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_metadata_refresh_due
ON public.people (
    GREATEST(updated_at, COALESCE(metadata_refresh_attempted_at, updated_at)),
    id
)
WHERE tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_people_metadata_refresh_due;

ALTER TABLE public.people
    DROP COLUMN IF EXISTS metadata_refresh_attempted_at;
