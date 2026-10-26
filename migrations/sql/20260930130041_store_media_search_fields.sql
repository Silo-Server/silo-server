-- +goose NO TRANSACTION

-- +goose Up
-- Nullable additions avoid a table rewrite under ACCESS EXCLUSIVE. Install
-- synchronous maintenance before backfilling so concurrent inserts and edits
-- never leave a stale search document.
ALTER TABLE public.media_items
    ADD COLUMN IF NOT EXISTS original_title_normalized text,
    ADD COLUMN IF NOT EXISTS sort_title_normalized text,
    ADD COLUMN IF NOT EXISTS search_title_vector tsvector,
    ADD COLUMN IF NOT EXISTS search_overview_vector tsvector;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.set_media_item_search_fields()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT'
       OR NEW.title IS DISTINCT FROM OLD.title
       OR NEW.original_title IS DISTINCT FROM OLD.original_title
       OR NEW.sort_title IS DISTINCT FROM OLD.sort_title
       OR NEW.search_title_vector IS NULL
       OR NEW.original_title_normalized IS NULL
       OR NEW.sort_title_normalized IS NULL THEN
        NEW.original_title_normalized := public.normalize_search_text(NEW.original_title);
        NEW.sort_title_normalized := public.normalize_search_text(NEW.sort_title);
        NEW.search_title_vector :=
            setweight(to_tsvector('simple', public.normalize_search_text(NEW.title)), 'A') ||
            setweight(to_tsvector('simple', NEW.original_title_normalized), 'A') ||
            setweight(to_tsvector('simple', NEW.sort_title_normalized), 'B');
    END IF;
    IF TG_OP = 'INSERT'
       OR NEW.overview IS DISTINCT FROM OLD.overview
       OR NEW.search_overview_vector IS NULL THEN
        NEW.search_overview_vector := to_tsvector('english', COALESCE(NEW.overview, ''));
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Re-ID can move an unfilled row behind the backfill's primary-key boundary.
-- Populate that document during the move and retain already-populated fields.
-- Replacement also repairs an older trigger on a resumed migration.
CREATE OR REPLACE TRIGGER trg_media_items_search_fields
BEFORE INSERT OR UPDATE OF content_id, title, original_title, sort_title, overview
ON public.media_items FOR EACH ROW
EXECUTE FUNCTION public.set_media_item_search_fields();

-- Commit each batch to bound row-lock retention, WAL bursts, and replication
-- lag. The primary-key scan visits existing rows once; interrupted migrations
-- resume by skipping documents already populated. The procedure is called as
-- a top-level statement because transaction control is prohibited in a DO.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE public.backfill_media_item_search_fields()
LANGUAGE plpgsql
AS $$
DECLARE
    last_id text;
    batch_ids text[];
BEGIN
    LOOP
        -- Separate the first scan from continuation so a cached generic plan
        -- keeps the primary-key range condition instead of a nullable OR.
        IF last_id IS NULL THEN
            SELECT array_agg(content_id ORDER BY content_id) INTO batch_ids
            FROM (
                SELECT content_id FROM public.media_items
                ORDER BY content_id LIMIT 1000
            ) batch;
        ELSE
            SELECT array_agg(content_id ORDER BY content_id) INTO batch_ids
            FROM (
                SELECT content_id FROM public.media_items WHERE content_id > last_id
                ORDER BY content_id LIMIT 1000
            ) batch;
        END IF;
        EXIT WHEN batch_ids IS NULL;
        UPDATE public.media_items
        SET original_title_normalized = public.normalize_search_text(original_title),
            sort_title_normalized = public.normalize_search_text(sort_title),
            search_title_vector =
                setweight(to_tsvector('simple', public.normalize_search_text(title)), 'A') ||
                setweight(to_tsvector('simple', public.normalize_search_text(original_title)), 'A') ||
                setweight(to_tsvector('simple', public.normalize_search_text(sort_title)), 'B'),
            search_overview_vector = to_tsvector('english', COALESCE(overview, ''))
        WHERE content_id = ANY(batch_ids)
          AND (search_title_vector IS NULL OR search_overview_vector IS NULL
               OR original_title_normalized IS NULL OR sort_title_normalized IS NULL);
        last_id := batch_ids[array_length(batch_ids, 1)];
        COMMIT;
    END LOOP;
END;
$$;
-- +goose StatementEnd
CALL public.backfill_media_item_search_fields();
DROP PROCEDURE public.backfill_media_item_search_fields();

-- A failed concurrent build can leave an invalid index behind. Remove it
-- before retrying IF NOT EXISTS so a resumed migration creates usable indexes.
-- +goose StatementBegin
DO $$
DECLARE
    index_name text;
BEGIN
    FOREACH index_name IN ARRAY ARRAY[
        'idx_media_items_stored_search_title',
        'idx_media_items_stored_search_overview'
    ] LOOP
        IF EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            JOIN pg_index i ON i.indexrelid = c.oid
            WHERE n.nspname = 'public' AND c.relname = index_name AND NOT i.indisvalid
        ) THEN
            EXECUTE format('DROP INDEX public.%I', index_name);
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_stored_search_title
    ON public.media_items USING gin (search_title_vector);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_stored_search_overview
    ON public.media_items USING gin (search_overview_vector);

-- No current query reads the predecessor expression indexes, and each one
-- recomputes its document on every title or overview write. A pre-upgrade
-- binary still issuing expression queries returns the same results without
-- them, only slower; Silo does not support mixed-version fleets.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_search_title_fields;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_search_overview;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_search_title_fields
    ON public.media_items USING gin ((
        setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(title, ''))), 'A') ||
        setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(original_title, ''))), 'A') ||
        setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(sort_title, ''))), 'B')
    ));
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_search_overview
    ON public.media_items USING gin (to_tsvector('english', COALESCE(overview, '')));
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_stored_search_title;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_stored_search_overview;
DROP TRIGGER IF EXISTS trg_media_items_search_fields ON public.media_items;
DROP FUNCTION IF EXISTS public.set_media_item_search_fields();
ALTER TABLE public.media_items
    DROP COLUMN IF EXISTS search_overview_vector,
    DROP COLUMN IF EXISTS search_title_vector,
    DROP COLUMN IF EXISTS sort_title_normalized,
    DROP COLUMN IF EXISTS original_title_normalized;
