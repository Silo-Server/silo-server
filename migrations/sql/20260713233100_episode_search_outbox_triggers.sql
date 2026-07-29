-- +goose Up
-- +goose StatementBegin

CREATE OR REPLACE FUNCTION public.catalog_search_capture_enabled()
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT lower(COALESCE(
        (SELECT value FROM public.server_settings WHERE key = 'catalog.search.provider'),
        'postgres'
    )) = 'meilisearch'
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_episode_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', content_id, '' FROM new_rows GROUP BY content_id;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_episode_update()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'delete', old_rows.content_id, ''
    FROM old_rows
    LEFT JOIN new_rows ON new_rows.content_id = old_rows.content_id
    WHERE new_rows.content_id IS NULL
    GROUP BY old_rows.content_id;

    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', content_id, '' FROM new_rows GROUP BY content_id;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_episode_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'delete', content_id, '' FROM old_rows GROUP BY content_id;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_episode_library_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', episode_id, '' FROM new_rows GROUP BY episode_id;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_episode_library_update()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', episode_id, ''
    FROM (
        SELECT episode_id FROM old_rows
        UNION
        SELECT episode_id FROM new_rows
    ) changed;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_episode_library_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', episode_id, '' FROM old_rows GROUP BY episode_id;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_item_library_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', content_id, '' FROM new_rows GROUP BY content_id;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_item_library_update()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', content_id, ''
    FROM (
        SELECT content_id FROM old_rows
        UNION
        SELECT content_id FROM new_rows
    ) changed;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_item_library_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', content_id, '' FROM old_rows GROUP BY content_id;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.catalog_search_capture_series_type_update()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT public.catalog_search_capture_enabled() THEN RETURN NULL; END IF;
    INSERT INTO public.catalog_search_index_events (provider, action, content_id, previous_content_id)
    SELECT 'meilisearch', 'upsert', e.content_id, ''
    FROM (
        SELECT new_rows.content_id
        FROM new_rows
        JOIN old_rows USING (content_id)
        WHERE new_rows.type IS DISTINCT FROM old_rows.type
          AND (new_rows.type = 'series' OR old_rows.type = 'series')
    ) changed
    JOIN public.episodes e ON e.series_id = changed.content_id
    GROUP BY e.content_id;
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_catalog_search_episode_insert
AFTER INSERT ON public.episodes
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_episode_insert();

CREATE TRIGGER trg_catalog_search_episode_update
AFTER UPDATE ON public.episodes
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_episode_update();

CREATE TRIGGER trg_catalog_search_episode_delete
AFTER DELETE ON public.episodes
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_episode_delete();

CREATE TRIGGER trg_catalog_search_episode_library_insert
AFTER INSERT ON public.episode_libraries
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_episode_library_insert();

CREATE TRIGGER trg_catalog_search_episode_library_update
AFTER UPDATE ON public.episode_libraries
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_episode_library_update();

CREATE TRIGGER trg_catalog_search_episode_library_delete
AFTER DELETE ON public.episode_libraries
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_episode_library_delete();

CREATE TRIGGER trg_catalog_search_item_library_insert
AFTER INSERT ON public.media_item_libraries
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_item_library_insert();

CREATE TRIGGER trg_catalog_search_item_library_update
AFTER UPDATE ON public.media_item_libraries
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_item_library_update();

CREATE TRIGGER trg_catalog_search_item_library_delete
AFTER DELETE ON public.media_item_libraries
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_item_library_delete();

CREATE TRIGGER trg_catalog_search_series_type_update
AFTER UPDATE ON public.media_items
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_search_capture_series_type_update();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_catalog_search_series_type_update ON public.media_items;
DROP TRIGGER IF EXISTS trg_catalog_search_item_library_delete ON public.media_item_libraries;
DROP TRIGGER IF EXISTS trg_catalog_search_item_library_update ON public.media_item_libraries;
DROP TRIGGER IF EXISTS trg_catalog_search_item_library_insert ON public.media_item_libraries;
DROP TRIGGER IF EXISTS trg_catalog_search_episode_library_delete ON public.episode_libraries;
DROP TRIGGER IF EXISTS trg_catalog_search_episode_library_update ON public.episode_libraries;
DROP TRIGGER IF EXISTS trg_catalog_search_episode_library_insert ON public.episode_libraries;
DROP TRIGGER IF EXISTS trg_catalog_search_episode_delete ON public.episodes;
DROP TRIGGER IF EXISTS trg_catalog_search_episode_update ON public.episodes;
DROP TRIGGER IF EXISTS trg_catalog_search_episode_insert ON public.episodes;

DROP FUNCTION IF EXISTS public.catalog_search_capture_series_type_update();
DROP FUNCTION IF EXISTS public.catalog_search_capture_item_library_delete();
DROP FUNCTION IF EXISTS public.catalog_search_capture_item_library_update();
DROP FUNCTION IF EXISTS public.catalog_search_capture_item_library_insert();
DROP FUNCTION IF EXISTS public.catalog_search_capture_episode_library_delete();
DROP FUNCTION IF EXISTS public.catalog_search_capture_episode_library_update();
DROP FUNCTION IF EXISTS public.catalog_search_capture_episode_library_insert();
DROP FUNCTION IF EXISTS public.catalog_search_capture_episode_delete();
DROP FUNCTION IF EXISTS public.catalog_search_capture_episode_update();
DROP FUNCTION IF EXISTS public.catalog_search_capture_episode_insert();
DROP FUNCTION IF EXISTS public.catalog_search_capture_enabled();
-- +goose StatementEnd
