-- +goose NO TRANSACTION

-- +goose Up
-- PostgreSQL search reads the already-maintained episode_catalog_entries table
-- instead of rebuilding vectors from episodes and probing episode_libraries for
-- every candidate. These nullable columns are metadata-only additions; the one
-- bounded UPDATE below builds the disk-backed search documents before the
-- indexes become visible to the application.
ALTER TABLE public.episode_catalog_entries
    ADD COLUMN IF NOT EXISTS search_title_normalized text,
    ADD COLUMN IF NOT EXISTS search_title_vector tsvector,
    ADD COLUMN IF NOT EXISTS search_overview_vector tsvector;

-- The existing episode-catalog refresh path always writes title during an
-- episode refresh. Hook that write so future episode/library changes keep the
-- search document synchronous without a queue, worker, or external RAM service.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.set_episode_catalog_entry_search_fields()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_overview text;
BEGIN
    SELECT COALESCE(e.overview, '')
    INTO source_overview
    FROM public.episodes e
    WHERE e.content_id = NEW.episode_id;

    NEW.search_title_normalized := public.normalize_search_text(NEW.title);
    NEW.search_title_vector := setweight(
        to_tsvector('simple', NEW.search_title_normalized),
        'A'
    );
    NEW.search_overview_vector := to_tsvector('english', COALESCE(source_overview, ''));
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_trigger
        WHERE tgname = 'trg_episode_catalog_entries_search_fields'
          AND tgrelid = 'public.episode_catalog_entries'::regclass
          AND NOT tgisinternal
    ) THEN
        EXECUTE 'CREATE TRIGGER trg_episode_catalog_entries_search_fields BEFORE INSERT OR UPDATE OF episode_id, title ON public.episode_catalog_entries FOR EACH ROW EXECUTE FUNCTION public.set_episode_catalog_entry_search_fields()';
    END IF;
END;
$$;
-- +goose StatementEnd

-- Migration 142's trigger already refreshes episode_catalog_entries for all
-- fields except overview. Keep that live trigger in place and add the missing
-- event separately: this migration must remain non-transactional for the
-- concurrent indexes below, so replacing the existing trigger would open a
-- committed write gap between DROP TRIGGER and CREATE TRIGGER.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_trigger
        WHERE tgname = 'trg_episode_catalog_entries_episodes_overview'
          AND tgrelid = 'public.episodes'::regclass
          AND NOT tgisinternal
    ) THEN
        EXECUTE 'CREATE TRIGGER trg_episode_catalog_entries_episodes_overview AFTER UPDATE OF overview ON public.episodes FOR EACH ROW EXECUTE FUNCTION public.episode_catalog_entries_episodes_trigger()';
    END IF;
END;
$$;
-- +goose StatementEnd

UPDATE public.episode_catalog_entries ece
SET
    search_title_normalized = public.normalize_search_text(ece.title),
    search_title_vector = setweight(
        to_tsvector('simple', public.normalize_search_text(ece.title)),
        'A'
    ),
    search_overview_vector = to_tsvector('english', COALESCE(e.overview, ''))
FROM public.episodes e
WHERE e.content_id = ece.episode_id
  AND (
      ece.search_title_normalized IS NULL
      OR ece.search_title_vector IS NULL
      OR ece.search_overview_vector IS NULL
  );

-- Short interactive queries use exact normalized-title lookups, while longer
-- queries use stored vectors. Build every index concurrently so the catalog
-- remains readable and writable throughout the index phase.
-- +goose StatementBegin
DO $$
DECLARE
    index_name text;
BEGIN
    FOREACH index_name IN ARRAY ARRAY[
        'idx_media_items_title_normalized_exact',
        'idx_media_item_aliases_normalized_content',
        'idx_episode_catalog_entries_search_title_normalized',
        'idx_episode_catalog_entries_search_title',
        'idx_episode_catalog_entries_search_overview',
        'idx_episode_catalog_entries_search_episode'
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

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_title_normalized_exact
    ON public.media_items (title_normalized text_pattern_ops, content_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_item_aliases_normalized_content
    ON public.media_item_aliases (normalized_title text_pattern_ops, content_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episode_catalog_entries_search_title_normalized
    ON public.episode_catalog_entries (search_title_normalized text_pattern_ops, episode_id, media_folder_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episode_catalog_entries_search_title
    ON public.episode_catalog_entries USING gin (search_title_vector);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episode_catalog_entries_search_overview
    ON public.episode_catalog_entries USING gin (search_overview_vector);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episode_catalog_entries_search_episode
    ON public.episode_catalog_entries (episode_id, media_folder_id);

-- +goose Down
DROP TRIGGER IF EXISTS trg_episode_catalog_entries_episodes_overview ON public.episodes;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_search_episode;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_search_overview;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_search_title;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_search_title_normalized;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_item_aliases_normalized_content;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_title_normalized_exact;

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_search_fields ON public.episode_catalog_entries;
DROP FUNCTION IF EXISTS public.set_episode_catalog_entry_search_fields();

ALTER TABLE public.episode_catalog_entries
    DROP COLUMN IF EXISTS search_overview_vector,
    DROP COLUMN IF EXISTS search_title_vector,
    DROP COLUMN IF EXISTS search_title_normalized;
