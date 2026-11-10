-- +goose NO TRANSACTION

-- +goose Up
-- Install each trigger in its own committed statement, before the backfill.
-- Runtime belongs to active recording files; all file writers share this rule.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.refresh_audiobook_runtime(p_content_ids text[])
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    -- Most file writers handle movies or episodes. Exclude their items
    -- before reading media_files, including in a mixed batch.
    SELECT array_agg(content_id) INTO p_content_ids
    FROM public.media_items
    WHERE content_id = ANY(p_content_ids) AND type = 'audiobook'
      AND NOT (7 = ANY(COALESCE(locked_fields, '{}'::integer[])));

    IF COALESCE(cardinality(p_content_ids), 0) = 0 THEN
        RETURN;
    END IF;

    -- Lock before aggregating, in a separate statement: a waiter must see
    -- the preceding writer's committed files, not its pre-wait snapshot.
    -- NO KEY UPDATE permits the file and membership foreign-key checks.
    PERFORM 1 FROM public.media_items
    WHERE content_id = ANY(p_content_ids) AND type = 'audiobook'
      AND NOT (7 = ANY(COALESCE(locked_fields, '{}'::integer[])))
    ORDER BY content_id
    FOR NO KEY UPDATE;

    UPDATE public.media_items mi
    SET runtime = files.minutes, updated_at = NOW()
    FROM (
        SELECT content_id,
               GREATEST(ROUND(MAX(root_seconds) / 60.0), 1)::integer AS minutes
        FROM (
            SELECT content_id, SUM(duration) AS root_seconds
            FROM public.media_files
            WHERE content_id = ANY(p_content_ids)
              AND missing_since IS NULL AND duration > 0
            GROUP BY content_id, media_folder_id, canonical_root_path
        ) roots
        GROUP BY content_id
    ) files
    WHERE mi.content_id = files.content_id AND mi.type = 'audiobook'
      AND NOT (7 = ANY(COALESCE(mi.locked_fields, '{}'::integer[])))
      AND mi.runtime IS DISTINCT FROM files.minutes;
    -- No positive-duration copy leaves the stored value alone, including
    -- temporarily missing books. Field 7 is the existing runtime lock.
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.audiobook_item_file_stats_media_files_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- Item upserts already hold this row before their stats trigger runs.
    -- File writers must use that same order before touching shared stats.
    PERFORM 1 FROM public.media_items
    WHERE content_id = ANY(ARRAY[OLD.content_id, NEW.content_id])
      AND type = 'audiobook'
    ORDER BY content_id
    FOR NO KEY UPDATE;

    IF TG_OP = 'DELETE' THEN
        PERFORM public.refresh_audiobook_item_file_stats(OLD.media_folder_id, OLD.content_id);
        RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE'
       AND (OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id OR OLD.content_id IS DISTINCT FROM NEW.content_id) THEN
        PERFORM public.refresh_audiobook_item_file_stats(OLD.media_folder_id, OLD.content_id);
    END IF;

    PERFORM public.refresh_audiobook_item_file_stats(NEW.media_folder_id, NEW.content_id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.audiobook_runtime_media_files_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    ids text[];
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT array_agg(DISTINCT content_id) INTO ids FROM new_files;
    ELSIF TG_OP = 'DELETE' THEN
        SELECT array_agg(DISTINCT content_id) INTO ids FROM old_files;
    ELSE
        SELECT array_agg(DISTINCT content_id) INTO ids FROM (
            SELECT unnest(ARRAY[o.content_id, n.content_id]) AS content_id
            FROM old_files o JOIN new_files n USING (id)
            WHERE (o.content_id, o.media_folder_id, o.canonical_root_path, o.duration, o.missing_since)
               IS DISTINCT FROM
                  (n.content_id, n.media_folder_id, n.canonical_root_path, n.duration, n.missing_since)
        ) changed;
    END IF;
    PERFORM public.refresh_audiobook_runtime(ids);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Statement triggers aggregate a multipart write once, after its resulting
-- rows are available. Both old and new items are refreshed on a reassignment.
CREATE OR REPLACE TRIGGER audiobook_runtime_files_insert
AFTER INSERT ON public.media_files
REFERENCING NEW TABLE AS new_files
FOR EACH STATEMENT EXECUTE FUNCTION public.audiobook_runtime_media_files_trigger();

CREATE OR REPLACE TRIGGER audiobook_runtime_files_update
AFTER UPDATE ON public.media_files
REFERENCING OLD TABLE AS old_files NEW TABLE AS new_files
FOR EACH STATEMENT EXECUTE FUNCTION public.audiobook_runtime_media_files_trigger();

CREATE OR REPLACE TRIGGER audiobook_runtime_files_delete
AFTER DELETE ON public.media_files
REFERENCING OLD TABLE AS old_files
FOR EACH STATEMENT EXECUTE FUNCTION public.audiobook_runtime_media_files_trigger();

-- Replaying an interrupted run is safe: corrected values are not rewritten.
-- Each batch holds item locks for at most 100 books, and lock waits are bounded.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE public.backfill_audiobook_runtime_from_files()
LANGUAGE plpgsql
AS $$
DECLARE
    last_content_id text := '';
    item_ids text[];
BEGIN
    LOOP
        SELECT array_agg(content_id ORDER BY content_id) INTO item_ids
        FROM (
            SELECT content_id FROM public.media_items
            WHERE content_id > last_content_id AND type = 'audiobook'
              AND NOT (7 = ANY(COALESCE(locked_fields, '{}'::integer[])))
            ORDER BY content_id LIMIT 100
        ) batch;
        EXIT WHEN item_ids IS NULL;

        SET LOCAL lock_timeout = '5s';
        PERFORM public.refresh_audiobook_runtime(item_ids);
        last_content_id := item_ids[cardinality(item_ids)];
        COMMIT;
    END LOOP;
END;
$$;
-- +goose StatementEnd

CALL public.backfill_audiobook_runtime_from_files();
DROP PROCEDURE public.backfill_audiobook_runtime_from_files();

-- +goose Down
-- Replaced provider runtimes cannot be restored. Remove runtime maintenance
-- and restore the preceding stats trigger implementation.
DROP TRIGGER IF EXISTS audiobook_runtime_files_delete ON public.media_files;
DROP TRIGGER IF EXISTS audiobook_runtime_files_update ON public.media_files;
DROP TRIGGER IF EXISTS audiobook_runtime_files_insert ON public.media_files;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.audiobook_item_file_stats_media_files_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM public.refresh_audiobook_item_file_stats(OLD.media_folder_id, OLD.content_id);
        RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE'
       AND (OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id OR OLD.content_id IS DISTINCT FROM NEW.content_id) THEN
        PERFORM public.refresh_audiobook_item_file_stats(OLD.media_folder_id, OLD.content_id);
    END IF;

    PERFORM public.refresh_audiobook_item_file_stats(NEW.media_folder_id, NEW.content_id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS public.audiobook_runtime_media_files_trigger();
DROP FUNCTION IF EXISTS public.refresh_audiobook_runtime(text[]);
DROP PROCEDURE IF EXISTS public.backfill_audiobook_runtime_from_files();
