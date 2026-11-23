-- +goose Up
-- Metadata passes rewrite a series' episodes with the values they already
-- hold, and an upsert stamps updated_at even then. Queue a search upsert only
-- for an episode whose row changed in anything but updated_at, so a refresh
-- that changes nothing queues nothing. Comparing whole rows keeps columns
-- added later covered.
-- +goose StatementBegin
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
    SELECT 'meilisearch', 'upsert', new_rows.content_id, ''
    FROM new_rows
    LEFT JOIN old_rows ON old_rows.content_id = new_rows.content_id
    WHERE old_rows.content_id IS NULL
       OR to_jsonb(new_rows) - 'updated_at' IS DISTINCT FROM to_jsonb(old_rows) - 'updated_at'
    GROUP BY new_rows.content_id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
-- +goose StatementEnd
