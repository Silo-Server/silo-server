-- +goose NO TRANSACTION

-- +goose Up
-- A provider can reject more than one value for the same item, so the negative
-- cache keys on the value as well. Build the wider key concurrently: a plain
-- ADD PRIMARY KEY holds ACCESS EXCLUSIVE for the whole index build, blocking
-- reads and writes on a table the refresh path touches constantly. All three
-- key columns are already NOT NULL, so attaching the finished index is a
-- metadata-only step.
-- Remove INVALID remnants first: IF NOT EXISTS otherwise accepts a failed
-- concurrent build and Goose would record a primary key that cannot be used.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'stale_media_ids_pkey_new'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.stale_media_ids_pkey_new;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS stale_media_ids_pkey_new
ON public.stale_media_ids (content_id, provider, provider_id);

-- Between these two statements the table has no primary key, but
-- stale_media_ids_pkey_new already enforces uniqueness on the new key, so only
-- the newly permitted duplicate (content_id, provider) pairs can appear.
ALTER TABLE stale_media_ids
    DROP CONSTRAINT IF EXISTS stale_media_ids_pkey;

ALTER TABLE stale_media_ids
    ADD CONSTRAINT stale_media_ids_pkey PRIMARY KEY USING INDEX stale_media_ids_pkey_new;

-- +goose Down
-- Collapse back to one row per (content_id, provider), keeping the most
-- recently rejected value — what the single-value schema would have held.
-- Run this with writers stopped: without a surrounding transaction a concurrent
-- insert between the dedup and the index build fails the build.
WITH ranked AS (
    SELECT ctid,
           ROW_NUMBER() OVER (
               PARTITION BY content_id, provider
               ORDER BY last_seen_at DESC, first_seen_at ASC, provider_id ASC
           ) AS row_number
    FROM stale_media_ids
)
DELETE FROM stale_media_ids
USING ranked
WHERE stale_media_ids.ctid = ranked.ctid
  AND ranked.row_number > 1;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'stale_media_ids_pkey_old'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.stale_media_ids_pkey_old;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS stale_media_ids_pkey_old
ON public.stale_media_ids (content_id, provider);

ALTER TABLE stale_media_ids
    DROP CONSTRAINT IF EXISTS stale_media_ids_pkey;

ALTER TABLE stale_media_ids
    ADD CONSTRAINT stale_media_ids_pkey PRIMARY KEY USING INDEX stale_media_ids_pkey_old;
