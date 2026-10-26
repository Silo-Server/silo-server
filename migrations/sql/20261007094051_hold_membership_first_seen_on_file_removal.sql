-- +goose Up
-- A library membership leaves as soon as its item or episode has no present
-- file, so a release swapped for a new file (an arr upgrade, a file that
-- returns after a mount outage) used to come back with a fresh first_seen_at
-- and jump to the top of Recently Added. When a membership is deleted, its
-- first-seen data is held on the item's or episode's missing file rows in that
-- folder; when the membership is inserted again, the held value is restored
-- and consumed. The value lives exactly as long as those rows stay in the
-- trash, and it is dropped when a row is relinked or its item is deleted.
ALTER TABLE public.media_files
    ADD COLUMN held_item_first_seen_at timestamptz,
    ADD COLUMN held_episode_first_seen_at timestamptz,
    ADD COLUMN held_episode_first_seen_scan_run_id text
        REFERENCES public.scan_runs(id) ON DELETE SET NULL;

-- Every lookup below also filters on content_id or episode_id, which the
-- existing idx_media_files_content and idx_media_files_episode narrow to a
-- title's few files, so the held columns need no index of their own.

-- Only removals with a missing file left behind are held, and only while the
-- item or episode still exists: a cascade from deleting it holds nothing.
-- +goose StatementBegin
CREATE FUNCTION public.hold_item_library_first_seen()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE public.media_files mf
    SET held_item_first_seen_at = LEAST(mf.held_item_first_seen_at, o.first_seen_at)
    FROM old_rows o
    WHERE mf.content_id = o.content_id
      AND mf.media_folder_id = o.media_folder_id
      AND mf.missing_since IS NOT NULL
      AND EXISTS (SELECT 1 FROM public.media_items mi WHERE mi.content_id = o.content_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.hold_episode_library_first_seen()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE public.media_files mf
    SET held_episode_first_seen_at = LEAST(mf.held_episode_first_seen_at, o.first_seen_at),
        held_episode_first_seen_scan_run_id = CASE
            WHEN mf.held_episode_first_seen_at IS NULL OR o.first_seen_at < mf.held_episode_first_seen_at
                THEN o.first_seen_scan_run_id
            ELSE mf.held_episode_first_seen_scan_run_id
        END
    FROM old_rows o
    WHERE mf.episode_id = o.episode_id
      AND mf.media_folder_id = o.media_folder_id
      AND mf.missing_since IS NOT NULL
      AND EXISTS (SELECT 1 FROM public.episodes e WHERE e.content_id = o.episode_id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- Restoring runs before the row is written so every reader, including the
-- catalog search capture and RecomputeSeriesLatestEpisodeAdded, sees the kept
-- value. It covers inserts and updates that move a membership to another
-- title or library: metadata matching rebinds a new file's provisional item to
-- the existing one by moving its membership.
--
-- The AFTER trigger checks again, then clears the held value. It fires only
-- for rows actually written, so an insert that hits ON CONFLICT DO NOTHING
-- (still running BEFORE INSERT triggers) consumes nothing. Checking again
-- covers a concurrent removal: an insert whose BEFORE lookup ran while the
-- removal was uncommitted waits on the row and is written after the removal
-- commits, and the AFTER trigger's query then sees the held value.
-- +goose StatementBegin
CREATE FUNCTION public.restore_item_library_first_seen()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    held timestamptz;
BEGIN
    SELECT MIN(mf.held_item_first_seen_at) INTO held
    FROM public.media_files mf
    WHERE mf.content_id = NEW.content_id
      AND mf.media_folder_id = NEW.media_folder_id
      AND mf.held_item_first_seen_at IS NOT NULL;
    IF held IS NOT NULL THEN
        NEW.first_seen_at := LEAST(NEW.first_seen_at, held);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.restore_episode_library_first_seen()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    held timestamptz;
    held_run text;
BEGIN
    SELECT mf.held_episode_first_seen_at, mf.held_episode_first_seen_scan_run_id
    INTO held, held_run
    FROM public.media_files mf
    WHERE mf.episode_id = NEW.episode_id
      AND mf.media_folder_id = NEW.media_folder_id
      AND mf.held_episode_first_seen_at IS NOT NULL
    ORDER BY mf.held_episode_first_seen_at ASC, mf.id ASC
    LIMIT 1;
    IF held IS NOT NULL AND (NEW.first_seen_at IS NULL OR held < NEW.first_seen_at) THEN
        NEW.first_seen_at := held;
        NEW.first_seen_scan_run_id := held_run;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.consume_item_library_held_first_seen()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    held timestamptz;
BEGIN
    SELECT MIN(mf.held_item_first_seen_at) INTO held
    FROM public.media_files mf
    WHERE mf.content_id = NEW.content_id
      AND mf.media_folder_id = NEW.media_folder_id
      AND mf.held_item_first_seen_at IS NOT NULL;
    IF held IS NULL THEN
        RETURN NULL;
    END IF;
    IF held < NEW.first_seen_at THEN
        UPDATE public.media_item_libraries
        SET first_seen_at = held
        WHERE content_id = NEW.content_id
          AND media_folder_id = NEW.media_folder_id;
    END IF;
    UPDATE public.media_files
    SET held_item_first_seen_at = NULL
    WHERE content_id = NEW.content_id
      AND media_folder_id = NEW.media_folder_id
      AND held_item_first_seen_at IS NOT NULL;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.consume_episode_library_held_first_seen()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    held timestamptz;
    held_run text;
BEGIN
    SELECT mf.held_episode_first_seen_at, mf.held_episode_first_seen_scan_run_id
    INTO held, held_run
    FROM public.media_files mf
    WHERE mf.episode_id = NEW.episode_id
      AND mf.media_folder_id = NEW.media_folder_id
      AND mf.held_episode_first_seen_at IS NOT NULL
    ORDER BY mf.held_episode_first_seen_at ASC, mf.id ASC
    LIMIT 1;
    IF held IS NULL THEN
        RETURN NULL;
    END IF;
    IF NEW.first_seen_at IS NULL OR held < NEW.first_seen_at THEN
        UPDATE public.episode_libraries
        SET first_seen_at = held,
            first_seen_scan_run_id = held_run
        WHERE episode_id = NEW.episode_id
          AND media_folder_id = NEW.media_folder_id;
    END IF;
    UPDATE public.media_files
    SET held_episode_first_seen_at = NULL,
        held_episode_first_seen_scan_run_id = NULL
    WHERE episode_id = NEW.episode_id
      AND media_folder_id = NEW.media_folder_id
      AND held_episode_first_seen_at IS NOT NULL;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- A held value belongs to the item and episode the row pointed at when it was
-- held; relinking the row or moving it to another library drops it. An
-- episode change also drops the item's value, which covers rows whose episode
-- was deleted with their series (see drop_held_first_seen_on_item_delete).
-- +goose StatementBegin
CREATE FUNCTION public.drop_held_first_seen_on_relink()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.content_id IS DISTINCT FROM OLD.content_id
        OR NEW.episode_id IS DISTINCT FROM OLD.episode_id
        OR NEW.media_folder_id IS DISTINCT FROM OLD.media_folder_id THEN
        NEW.held_item_first_seen_at := NULL;
    END IF;
    IF NEW.episode_id IS DISTINCT FROM OLD.episode_id
        OR NEW.media_folder_id IS DISTINCT FROM OLD.media_folder_id THEN
        NEW.held_episode_first_seen_at := NULL;
        NEW.held_episode_first_seen_scan_run_id := NULL;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- An item deleted after its hold ends (or by any other path) is gone for
-- good: a later item with the same content ID is a new arrival. This trigger
-- runs before the cascade from a deleted series sets its file rows'
-- episode_id to NULL, and updating such a row would fail its foreign key
-- check, so it skips them; that SET NULL clears their value instead.
-- +goose StatementBegin
CREATE FUNCTION public.drop_held_first_seen_on_item_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE public.media_files mf
    SET held_item_first_seen_at = NULL
    FROM old_rows o
    WHERE mf.content_id = o.content_id
      AND mf.held_item_first_seen_at IS NOT NULL
      AND (mf.episode_id IS NULL
          OR EXISTS (SELECT 1 FROM public.episodes e WHERE e.content_id = mf.episode_id));
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER media_item_libraries_hold_first_seen
AFTER DELETE ON public.media_item_libraries
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.hold_item_library_first_seen();

CREATE TRIGGER episode_libraries_hold_first_seen
AFTER DELETE ON public.episode_libraries
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.hold_episode_library_first_seen();

CREATE TRIGGER media_item_libraries_restore_first_seen
BEFORE INSERT ON public.media_item_libraries
FOR EACH ROW
EXECUTE FUNCTION public.restore_item_library_first_seen();

CREATE TRIGGER media_item_libraries_restore_first_seen_on_move
BEFORE UPDATE OF content_id, media_folder_id ON public.media_item_libraries
FOR EACH ROW
WHEN (OLD.content_id IS DISTINCT FROM NEW.content_id OR OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id)
EXECUTE FUNCTION public.restore_item_library_first_seen();

CREATE TRIGGER media_item_libraries_consume_held_first_seen
AFTER INSERT ON public.media_item_libraries
FOR EACH ROW
EXECUTE FUNCTION public.consume_item_library_held_first_seen();

CREATE TRIGGER media_item_libraries_consume_held_first_seen_on_move
AFTER UPDATE OF content_id, media_folder_id ON public.media_item_libraries
FOR EACH ROW
WHEN (OLD.content_id IS DISTINCT FROM NEW.content_id OR OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id)
EXECUTE FUNCTION public.consume_item_library_held_first_seen();

CREATE TRIGGER episode_libraries_restore_first_seen
BEFORE INSERT ON public.episode_libraries
FOR EACH ROW
EXECUTE FUNCTION public.restore_episode_library_first_seen();

CREATE TRIGGER episode_libraries_restore_first_seen_on_move
BEFORE UPDATE OF episode_id, media_folder_id ON public.episode_libraries
FOR EACH ROW
WHEN (OLD.episode_id IS DISTINCT FROM NEW.episode_id OR OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id)
EXECUTE FUNCTION public.restore_episode_library_first_seen();

CREATE TRIGGER episode_libraries_consume_held_first_seen
AFTER INSERT ON public.episode_libraries
FOR EACH ROW
EXECUTE FUNCTION public.consume_episode_library_held_first_seen();

CREATE TRIGGER episode_libraries_consume_held_first_seen_on_move
AFTER UPDATE OF episode_id, media_folder_id ON public.episode_libraries
FOR EACH ROW
WHEN (OLD.episode_id IS DISTINCT FROM NEW.episode_id OR OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id)
EXECUTE FUNCTION public.consume_episode_library_held_first_seen();

CREATE TRIGGER media_files_drop_held_first_seen
BEFORE UPDATE OF content_id, episode_id, media_folder_id ON public.media_files
FOR EACH ROW
WHEN (
    OLD.content_id IS DISTINCT FROM NEW.content_id
    OR OLD.episode_id IS DISTINCT FROM NEW.episode_id
    OR OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id
)
EXECUTE FUNCTION public.drop_held_first_seen_on_relink();

CREATE TRIGGER media_items_drop_held_first_seen
AFTER DELETE ON public.media_items
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.drop_held_first_seen_on_item_delete();

-- +goose Down
DROP TRIGGER media_items_drop_held_first_seen ON public.media_items;
DROP TRIGGER media_files_drop_held_first_seen ON public.media_files;
DROP TRIGGER episode_libraries_consume_held_first_seen_on_move ON public.episode_libraries;
DROP TRIGGER episode_libraries_consume_held_first_seen ON public.episode_libraries;
DROP TRIGGER episode_libraries_restore_first_seen_on_move ON public.episode_libraries;
DROP TRIGGER episode_libraries_restore_first_seen ON public.episode_libraries;
DROP TRIGGER media_item_libraries_consume_held_first_seen_on_move ON public.media_item_libraries;
DROP TRIGGER media_item_libraries_consume_held_first_seen ON public.media_item_libraries;
DROP TRIGGER media_item_libraries_restore_first_seen_on_move ON public.media_item_libraries;
DROP TRIGGER media_item_libraries_restore_first_seen ON public.media_item_libraries;
DROP TRIGGER episode_libraries_hold_first_seen ON public.episode_libraries;
DROP TRIGGER media_item_libraries_hold_first_seen ON public.media_item_libraries;
DROP FUNCTION public.drop_held_first_seen_on_item_delete();
DROP FUNCTION public.drop_held_first_seen_on_relink();
DROP FUNCTION public.consume_episode_library_held_first_seen();
DROP FUNCTION public.consume_item_library_held_first_seen();
DROP FUNCTION public.restore_episode_library_first_seen();
DROP FUNCTION public.restore_item_library_first_seen();
DROP FUNCTION public.hold_episode_library_first_seen();
DROP FUNCTION public.hold_item_library_first_seen();
ALTER TABLE public.media_files
    DROP COLUMN held_episode_first_seen_scan_run_id,
    DROP COLUMN held_episode_first_seen_at,
    DROP COLUMN held_item_first_seen_at;
