-- +goose NO TRANSACTION

-- +goose Up
-- This ALTER is intentionally its own autocommitted statement so its table
-- lock is released before the potentially large backfill runs separately.
ALTER TABLE ebook_enrichment_state
    ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS priority integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS lease_until timestamptz,
    ADD COLUMN IF NOT EXISTS claim_token text,
    ADD COLUMN IF NOT EXISTS requeue_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS protected_fields text[] NOT NULL DEFAULT '{}'::text[],
    ADD COLUMN IF NOT EXISTS last_attempt_at timestamptz,
    ADD COLUMN IF NOT EXISTS completed_at timestamptz,
    ADD COLUMN IF NOT EXISTS outcome text,
    ADD COLUMN IF NOT EXISTS last_error_class text,
    ADD COLUMN IF NOT EXISTS last_error text;

ALTER TABLE ebook_enrichment_state
    DROP CONSTRAINT IF EXISTS ebook_enrichment_state_status_check,
    DROP CONSTRAINT IF EXISTS ebook_enrichment_state_attempts_check;

ALTER TABLE ebook_enrichment_state
    ADD CONSTRAINT ebook_enrichment_state_status_check
        CHECK (status IN ('pending', 'running', 'discarded')),
    ADD CONSTRAINT ebook_enrichment_state_attempts_check
        CHECK (attempts >= 0);

-- Existing failure rows are part of the legacy backlog. Refreshed rows retain
-- their refresh date as the start of the 90-day schedule; unrefreshed rows keep
-- their old failure count and enter at legacy priority -100.
UPDATE ebook_enrichment_state state
SET status = 'pending',
    priority = CASE WHEN mi.last_refreshed IS NULL THEN -100 ELSE 0 END,
    attempts = state.failures,
    next_attempt_at = CASE
        WHEN mi.last_refreshed IS NULL THEN now()
        ELSE GREATEST(mi.last_refreshed + interval '90 days', now())
    END,
    completed_at = mi.last_refreshed,
    protected_fields = ARRAY_REMOVE(ARRAY[
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.title, '')) <> '' THEN 'title' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND COALESCE(mi.year, 0) > 0 THEN 'year' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.overview, '')) <> '' THEN 'overview' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.tagline, '')) <> '' THEN 'tagline' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.content_rating, '')) <> '' THEN 'content_rating' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND COALESCE(mi.runtime, 0) > 0 THEN 'runtime' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.release_date::text, '')) <> '' THEN 'release_date' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND cardinality(COALESCE(mi.genres, '{}'::text[])) > 0 THEN 'genres' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND cardinality(COALESCE(mi.studios, '{}'::text[])) > 0 THEN 'studios' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND EXISTS (
            SELECT 1 FROM item_people ip WHERE ip.content_id = mi.content_id AND ip.kind = 7
        ) THEN 'authors' END,
        CASE WHEN trim(COALESCE(mi.poster_path, '')) <> ''
            AND lower(trim(mi.poster_path)) NOT LIKE 'http://%'
            AND lower(trim(mi.poster_path)) NOT LIKE 'https://%'
            AND lower(trim(mi.poster_path)) NOT LIKE 'ebook-metadata/ebooks/%'
            THEN 'poster_path' END,
        CASE WHEN trim(COALESCE(mi.backdrop_path, '')) <> ''
            AND lower(trim(mi.backdrop_path)) NOT LIKE 'http://%'
            AND lower(trim(mi.backdrop_path)) NOT LIKE 'https://%'
            AND lower(trim(mi.backdrop_path)) NOT LIKE 'ebook-metadata/ebooks/%'
            THEN 'backdrop_path' END,
        CASE WHEN trim(COALESCE(mi.logo_path, '')) <> ''
            AND lower(trim(mi.logo_path)) NOT LIKE 'http://%'
            AND lower(trim(mi.logo_path)) NOT LIKE 'https://%'
            AND lower(trim(mi.logo_path)) NOT LIKE 'ebook-metadata/ebooks/%'
            THEN 'logo_path' END
    ]::text[], NULL),
    updated_at = now()
FROM media_items mi
WHERE mi.content_id = state.content_id;

-- Snapshot every pre-migration standalone ebook. Refreshed rows become normal
-- 90-day refresh work; only the unrefreshed legacy backlog starts at -100.
INSERT INTO ebook_enrichment_state (
    content_id,
    status,
    priority,
    attempts,
    next_attempt_at,
    completed_at,
    protected_fields,
    updated_at
)
SELECT
    mi.content_id,
    'pending',
    CASE WHEN mi.last_refreshed IS NULL THEN -100 ELSE 0 END,
    0,
    CASE
        WHEN mi.last_refreshed IS NULL THEN now()
        ELSE GREATEST(mi.last_refreshed + interval '90 days', now())
    END,
    mi.last_refreshed,
    ARRAY_REMOVE(ARRAY[
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.title, '')) <> '' THEN 'title' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND COALESCE(mi.year, 0) > 0 THEN 'year' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.overview, '')) <> '' THEN 'overview' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.tagline, '')) <> '' THEN 'tagline' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.content_rating, '')) <> '' THEN 'content_rating' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND COALESCE(mi.runtime, 0) > 0 THEN 'runtime' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.release_date::text, '')) <> '' THEN 'release_date' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND cardinality(COALESCE(mi.genres, '{}'::text[])) > 0 THEN 'genres' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND cardinality(COALESCE(mi.studios, '{}'::text[])) > 0 THEN 'studios' END,
        CASE WHEN lower(trim(mi.status)) = 'pending' AND EXISTS (
            SELECT 1 FROM item_people ip WHERE ip.content_id = mi.content_id AND ip.kind = 7
        ) THEN 'authors' END,
        CASE WHEN trim(COALESCE(mi.poster_path, '')) <> ''
            AND lower(trim(mi.poster_path)) NOT LIKE 'http://%'
            AND lower(trim(mi.poster_path)) NOT LIKE 'https://%'
            AND lower(trim(mi.poster_path)) NOT LIKE 'ebook-metadata/ebooks/%'
            THEN 'poster_path' END,
        CASE WHEN trim(COALESCE(mi.backdrop_path, '')) <> ''
            AND lower(trim(mi.backdrop_path)) NOT LIKE 'http://%'
            AND lower(trim(mi.backdrop_path)) NOT LIKE 'https://%'
            AND lower(trim(mi.backdrop_path)) NOT LIKE 'ebook-metadata/ebooks/%'
            THEN 'backdrop_path' END,
        CASE WHEN trim(COALESCE(mi.logo_path, '')) <> ''
            AND lower(trim(mi.logo_path)) NOT LIKE 'http://%'
            AND lower(trim(mi.logo_path)) NOT LIKE 'https://%'
            AND lower(trim(mi.logo_path)) NOT LIKE 'ebook-metadata/ebooks/%'
            THEN 'logo_path' END
    ]::text[], NULL),
    now()
FROM media_items mi
WHERE mi.type = 'ebook'
  AND NOT EXISTS (
      SELECT 1
      FROM manga_chapters mc
      WHERE mc.chapter_content_id = mi.content_id
  )
ON CONFLICT (content_id) DO NOTHING;

-- A crashed CREATE INDEX CONCURRENTLY can leave an INVALID index which blocks
-- an IF NOT EXISTS retry. Clean each lane-specific index before rebuilding it.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'ebook_enrichment_incremental_due_idx'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.ebook_enrichment_incremental_due_idx;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'ebook_enrichment_incremental_priority_idx'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.ebook_enrichment_incremental_priority_idx;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'ebook_enrichment_legacy_due_idx'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.ebook_enrichment_legacy_due_idx;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'ebook_enrichment_legacy_priority_idx'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.ebook_enrichment_legacy_priority_idx;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS ebook_enrichment_incremental_due_idx
    ON public.ebook_enrichment_state (next_attempt_at, updated_at, priority DESC)
    WHERE status IN ('pending', 'running') AND priority >= 0;

CREATE INDEX CONCURRENTLY IF NOT EXISTS ebook_enrichment_incremental_priority_idx
    ON public.ebook_enrichment_state (priority DESC, next_attempt_at, updated_at)
    WHERE status IN ('pending', 'running') AND priority >= 0;

CREATE INDEX CONCURRENTLY IF NOT EXISTS ebook_enrichment_legacy_due_idx
    ON public.ebook_enrichment_state (next_attempt_at, updated_at, priority DESC)
    WHERE status IN ('pending', 'running') AND priority < 0;

CREATE INDEX CONCURRENTLY IF NOT EXISTS ebook_enrichment_legacy_priority_idx
    ON public.ebook_enrichment_state (priority DESC, next_attempt_at, updated_at)
    WHERE status IN ('pending', 'running') AND priority < 0;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS ebook_enrichment_legacy_priority_idx;
DROP INDEX CONCURRENTLY IF EXISTS ebook_enrichment_legacy_due_idx;
DROP INDEX CONCURRENTLY IF EXISTS ebook_enrichment_incremental_priority_idx;
DROP INDEX CONCURRENTLY IF EXISTS ebook_enrichment_incremental_due_idx;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'ebook_enrichment_state'
          AND column_name = 'attempts'
    ) THEN
        UPDATE ebook_enrichment_state
        SET failures = attempts
        WHERE outcome = 'failed';

        -- Success and no-match rows have no representation in the old
        -- failure-only schema. Keep pending, skipped, discarded, and failed
        -- rows; old code can faithfully interpret their copied failure count.
        DELETE FROM ebook_enrichment_state
        WHERE outcome IN ('success', 'no_match');
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE ebook_enrichment_state
    DROP CONSTRAINT IF EXISTS ebook_enrichment_state_attempts_check,
    DROP CONSTRAINT IF EXISTS ebook_enrichment_state_status_check,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS last_error_class,
    DROP COLUMN IF EXISTS outcome,
    DROP COLUMN IF EXISTS completed_at,
    DROP COLUMN IF EXISTS last_attempt_at,
    DROP COLUMN IF EXISTS protected_fields,
    DROP COLUMN IF EXISTS requeue_requested,
    DROP COLUMN IF EXISTS claim_token,
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS attempts,
    DROP COLUMN IF EXISTS priority,
    DROP COLUMN IF EXISTS status;
