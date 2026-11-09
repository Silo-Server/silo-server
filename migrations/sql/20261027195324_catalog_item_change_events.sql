-- +goose Up
-- +goose StatementBegin

-- Outbox for the administrator catalog.item.changed events: added and removed
-- when an item gains or loses a library membership, files_changed when one of
-- its versions is added, removed or replaced. Triggers write it whatever code
-- path changed the rows; every API server drains it (see
-- internal/catalog/item_change_outbox.go).
CREATE TABLE public.catalog_item_change_events (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    content_id text COLLATE "C" NOT NULL,
    library_id integer NOT NULL,
    change     text NOT NULL CHECK (change IN ('added', 'removed', 'files_changed')),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A drain claims every row of the keys it takes, so later rows for the same
-- item and library coalesce into the one event.
CREATE INDEX catalog_item_change_events_key_idx
    ON public.catalog_item_change_events (content_id, library_id);

-- One row: when a server last drained. Draining waits a fixed interval after
-- it, so the cluster as a whole publishes at a bounded rate however many
-- servers run.
CREATE TABLE public.catalog_item_change_drain (
    singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    drained_at timestamptz NOT NULL DEFAULT '-infinity'
);
INSERT INTO public.catalog_item_change_drain DEFAULT VALUES;

-- catalog_item_change_queue writes one row per distinct (id, library). A row
-- names an episode when it has an episode id, else an item. It skips what the
-- administrator catalog listing never returns: books, podcasts and their
-- episodes, and, for files, any non-episode file not on a movie. An item
-- already deleted still gets its row, since it can no longer be checked.
CREATE FUNCTION public.catalog_item_change_queue(
    queue_change text, item_ids text[], episode_ids text[], library_ids integer[]
)
RETURNS void
LANGUAGE sql
AS $$
    -- Scalar lookups by primary key, not joins: a bulk statement then costs
    -- one or two index probes per row, and the planner cannot turn a
    -- misestimated row count into a scan of the catalog per row.
    INSERT INTO public.catalog_item_change_events (content_id, library_id, change)
    SELECT DISTINCT coalesce(q.episode_id, q.item_id), q.library_id, queue_change
    FROM unnest(item_ids, episode_ids, library_ids) AS q(item_id, episode_id, library_id)
    WHERE CASE
        WHEN q.episode_id IS NOT NULL THEN coalesce((
            SELECT s.type FROM public.media_items s
            WHERE s.content_id = (SELECT e.series_id FROM public.episodes e WHERE e.content_id = q.episode_id)
        ), 'series') = 'series'
        WHEN q.item_id IS NOT NULL THEN coalesce((
            SELECT m.type FROM public.media_items m WHERE m.content_id = q.item_id
        ), 'movie') IN ('movie', CASE WHEN queue_change = 'files_changed' THEN 'movie' ELSE 'series' END)
        ELSE false
    END
$$;

-- Membership: an inserted row is an add, a deleted row a removal. An update
-- compares the statement's (item, library) pairs, so a key move (the ON
-- UPDATE CASCADE from a re-keyed item, or a library move) is a removal of the
-- old pair and an add of the new one, and an update that keeps the pair is
-- nothing.
CREATE FUNCTION public.catalog_item_change_item_libraries()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM public.catalog_item_change_queue('added', array_agg(content_id), NULL, array_agg(media_folder_id))
        FROM new_rows;
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM public.catalog_item_change_queue('removed', array_agg(content_id), NULL, array_agg(media_folder_id))
        FROM old_rows;
    ELSE
        PERFORM public.catalog_item_change_queue('removed', array_agg(o.content_id), NULL, array_agg(o.media_folder_id))
        FROM old_rows o
        WHERE NOT EXISTS (
            SELECT 1 FROM new_rows n WHERE n.content_id = o.content_id AND n.media_folder_id = o.media_folder_id);
        PERFORM public.catalog_item_change_queue('added', array_agg(n.content_id), NULL, array_agg(n.media_folder_id))
        FROM new_rows n
        WHERE NOT EXISTS (
            SELECT 1 FROM old_rows o WHERE o.content_id = n.content_id AND o.media_folder_id = n.media_folder_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION public.catalog_item_change_episode_libraries()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM public.catalog_item_change_queue('added', NULL, array_agg(episode_id), array_agg(media_folder_id))
        FROM new_rows;
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM public.catalog_item_change_queue('removed', NULL, array_agg(episode_id), array_agg(media_folder_id))
        FROM old_rows;
    ELSE
        PERFORM public.catalog_item_change_queue('removed', NULL, array_agg(o.episode_id), array_agg(o.media_folder_id))
        FROM old_rows o
        WHERE NOT EXISTS (
            SELECT 1 FROM new_rows n WHERE n.episode_id = o.episode_id AND n.media_folder_id = o.media_folder_id);
        PERFORM public.catalog_item_change_queue('added', NULL, array_agg(n.episode_id), array_agg(n.media_folder_id))
        FROM new_rows n
        WHERE NOT EXISTS (
            SELECT 1 FROM old_rows o WHERE o.episode_id = n.episode_id AND o.media_folder_id = n.media_folder_id);
    END IF;
    RETURN NULL;
END;
$$;

-- Versions: a present (not missing), non-extra file. Inserts and deletes run
-- per statement; an update runs per row, and only when its WHEN clause sees a
-- change to what a version shows, so the scanner's routine writes queue
-- nothing (a trigger with transition tables cannot take a column list). An
-- update queues the old owner and the new one, which differ when the file
-- moved to another item or stopped or started counting as a version.
CREATE FUNCTION public.catalog_item_change_media_files()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM public.catalog_item_change_queue('files_changed', array_agg(content_id), array_agg(episode_id), array_agg(media_folder_id))
        FROM new_rows
        WHERE extra_id IS NULL AND missing_since IS NULL;
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM public.catalog_item_change_queue('files_changed', array_agg(content_id), array_agg(episode_id), array_agg(media_folder_id))
        FROM old_rows
        WHERE extra_id IS NULL AND missing_since IS NULL;
    ELSE
        PERFORM public.catalog_item_change_queue('files_changed', array_agg(f.content_id), array_agg(f.episode_id), array_agg(f.media_folder_id))
        FROM (VALUES
            (OLD.content_id, OLD.episode_id, OLD.media_folder_id, OLD.extra_id, OLD.missing_since),
            (NEW.content_id, NEW.episode_id, NEW.media_folder_id, NEW.extra_id, NEW.missing_since)
        ) AS f(content_id, episode_id, media_folder_id, extra_id, missing_since)
        WHERE f.extra_id IS NULL AND f.missing_since IS NULL;
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER trg_catalog_item_change_item_library_insert
AFTER INSERT ON public.media_item_libraries
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_item_change_item_libraries();

CREATE TRIGGER trg_catalog_item_change_item_library_update
AFTER UPDATE ON public.media_item_libraries
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_item_change_item_libraries();

CREATE TRIGGER trg_catalog_item_change_item_library_delete
AFTER DELETE ON public.media_item_libraries
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_item_change_item_libraries();

CREATE TRIGGER trg_catalog_item_change_episode_library_insert
AFTER INSERT ON public.episode_libraries
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_item_change_episode_libraries();

CREATE TRIGGER trg_catalog_item_change_episode_library_update
AFTER UPDATE ON public.episode_libraries
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_item_change_episode_libraries();

CREATE TRIGGER trg_catalog_item_change_episode_library_delete
AFTER DELETE ON public.episode_libraries
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_item_change_episode_libraries();

CREATE TRIGGER trg_catalog_item_change_media_file_insert
AFTER INSERT ON public.media_files
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_item_change_media_files();

CREATE TRIGGER trg_catalog_item_change_media_file_delete
AFTER DELETE ON public.media_files
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT EXECUTE FUNCTION public.catalog_item_change_media_files();

CREATE TRIGGER trg_catalog_item_change_media_file_update
AFTER UPDATE OF content_id, episode_id, media_folder_id, file_path, extra_id, missing_since,
    file_size, resolution, codec_video, codec_audio, audio_channels, hdr, container, bitrate,
    edition_key, video_tracks, audio_tracks
ON public.media_files
FOR EACH ROW
WHEN (
    ((old.extra_id IS NULL AND old.missing_since IS NULL) OR (new.extra_id IS NULL AND new.missing_since IS NULL))
    AND (
        old.content_id IS DISTINCT FROM new.content_id
        OR old.episode_id IS DISTINCT FROM new.episode_id
        OR old.media_folder_id IS DISTINCT FROM new.media_folder_id
        OR old.file_path IS DISTINCT FROM new.file_path
        OR (old.extra_id IS NULL) <> (new.extra_id IS NULL)
        OR (old.missing_since IS NULL) <> (new.missing_since IS NULL)
        OR old.file_size IS DISTINCT FROM new.file_size
        OR old.resolution IS DISTINCT FROM new.resolution
        OR old.codec_video IS DISTINCT FROM new.codec_video
        OR old.codec_audio IS DISTINCT FROM new.codec_audio
        OR old.audio_channels IS DISTINCT FROM new.audio_channels
        OR old.hdr IS DISTINCT FROM new.hdr
        OR old.container IS DISTINCT FROM new.container
        OR old.bitrate IS DISTINCT FROM new.bitrate
        OR old.edition_key IS DISTINCT FROM new.edition_key
        OR old.video_tracks IS DISTINCT FROM new.video_tracks
        OR old.audio_tracks IS DISTINCT FROM new.audio_tracks
    )
)
EXECUTE FUNCTION public.catalog_item_change_media_files();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_catalog_item_change_media_file_update ON public.media_files;
DROP TRIGGER IF EXISTS trg_catalog_item_change_media_file_delete ON public.media_files;
DROP TRIGGER IF EXISTS trg_catalog_item_change_media_file_insert ON public.media_files;
DROP TRIGGER IF EXISTS trg_catalog_item_change_episode_library_delete ON public.episode_libraries;
DROP TRIGGER IF EXISTS trg_catalog_item_change_episode_library_update ON public.episode_libraries;
DROP TRIGGER IF EXISTS trg_catalog_item_change_episode_library_insert ON public.episode_libraries;
DROP TRIGGER IF EXISTS trg_catalog_item_change_item_library_delete ON public.media_item_libraries;
DROP TRIGGER IF EXISTS trg_catalog_item_change_item_library_update ON public.media_item_libraries;
DROP TRIGGER IF EXISTS trg_catalog_item_change_item_library_insert ON public.media_item_libraries;
DROP FUNCTION IF EXISTS public.catalog_item_change_media_files();
DROP FUNCTION IF EXISTS public.catalog_item_change_episode_libraries();
DROP FUNCTION IF EXISTS public.catalog_item_change_item_libraries();
DROP FUNCTION IF EXISTS public.catalog_item_change_queue(text, text[], text[], integer[]);
DROP TABLE IF EXISTS public.catalog_item_change_drain;
DROP TABLE IF EXISTS public.catalog_item_change_events;
-- +goose StatementEnd
