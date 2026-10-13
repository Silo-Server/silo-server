-- +goose NO TRANSACTION
-- +goose Up
-- Record what each person lookup found, so a person the provider has already
-- answered is not retried on a short cadence just because a field such as the
-- bio is empty. metadata_refresh_due_at is when the background sweep may look
-- the person up again: NULL before the first lookup, and NULL again once a
-- person the providers do not know has used up its retries.
ALTER TABLE public.people
    ADD COLUMN IF NOT EXISTS metadata_refresh_outcome text,
    ADD COLUMN IF NOT EXISTS metadata_refresh_failures integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS metadata_refresh_due_at timestamptz;

-- Nothing records whether an earlier lookup was answered: credit enrichment
-- and photo caching also move updated_at past an attempt. Only a person whose
-- bio, photo and birth date are all filled lacks nothing a recheck would
-- fill, so only they wait the 90 days of an answered person.
UPDATE public.people
SET metadata_refresh_outcome = 'answered',
    metadata_refresh_failures = 0,
    metadata_refresh_due_at = updated_at + interval '90 days'
WHERE metadata_refresh_attempted_at IS NOT NULL
  AND metadata_refresh_outcome IS NULL
  AND updated_at >= metadata_refresh_attempted_at
  AND COALESCE(bio, '') <> ''
  AND COALESCE(photo_path, '') <> ''
  AND birth_date IS NOT NULL;

-- The rest may have found nothing, failed, or been answered without filling a
-- gap, and the old rule would retry them a week after the attempt. Keep that
-- one retry; its outcome then decides the next.
UPDATE public.people
SET metadata_refresh_outcome = 'failed',
    metadata_refresh_failures = 1,
    metadata_refresh_due_at = metadata_refresh_attempted_at + interval '7 days'
WHERE metadata_refresh_attempted_at IS NOT NULL
  AND metadata_refresh_outcome IS NULL;

-- A failed concurrent build can leave an invalid index that blocks an
-- IF NOT EXISTS retry, so discard only those unusable artifacts first.
-- +goose StatementBegin
DO $$
DECLARE
    index_name text;
BEGIN
    FOR index_name IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname IN ('idx_people_refresh_never_attempted', 'idx_people_refresh_due_at', 'idx_people_refresh_unrecorded')
          AND NOT i.indisvalid
    LOOP
        EXECUTE format('DROP INDEX public.%I', index_name);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- The sweep takes people never looked up, newest first, then people whose
-- next lookup is due, earliest first.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_refresh_never_attempted
ON public.people (id DESC)
WHERE metadata_refresh_attempted_at IS NULL
  AND (tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_refresh_due_at
ON public.people (metadata_refresh_due_at, id)
WHERE metadata_refresh_due_at IS NOT NULL
  AND (tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '');

-- During a rolling upgrade an older API server records an attempt without an
-- outcome or due time; the sweep retries those a week later, as it used to.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_refresh_unrecorded
ON public.people (metadata_refresh_attempted_at, id)
WHERE metadata_refresh_attempted_at IS NOT NULL
  AND metadata_refresh_outcome IS NULL
  AND metadata_refresh_due_at IS NULL
  AND (tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '');

DROP INDEX CONCURRENTLY IF EXISTS public.idx_people_metadata_refresh_due;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_metadata_refresh_due
ON public.people (
    GREATEST(updated_at, COALESCE(metadata_refresh_attempted_at, updated_at)),
    id
)
WHERE tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_people_refresh_unrecorded;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_people_refresh_due_at;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_people_refresh_never_attempted;

ALTER TABLE public.people
    DROP COLUMN IF EXISTS metadata_refresh_due_at,
    DROP COLUMN IF EXISTS metadata_refresh_failures,
    DROP COLUMN IF EXISTS metadata_refresh_outcome;
