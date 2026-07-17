-- +goose Up
CREATE TABLE public.artwork_revision_gc_candidates (
    id bigserial PRIMARY KEY,
    original_path text NOT NULL UNIQUE,
    -- Image type ("poster", "backdrop", ...) lets the collector expand the
    -- expected object keys in Go (artworkkey.ObjectKeys) when no exact
    -- manifest was tracked, so the variant ladder lives in one place.
    image_type text NOT NULL DEFAULT '',
    object_keys text[] NOT NULL DEFAULT '{}',
    not_before timestamptz NOT NULL,
    -- NULL means the revision is currently referenced and dormant. Artwork
    -- displacement triggers or the collector's dormant sweep reactivate it.
    next_attempt_at timestamptz,
    -- Set once the objects have been deleted from storage. The row then only
    -- survives until the post-delete reference heal succeeds, so a transient
    -- heal failure keeps a durable retry instead of orphaning broken pointers.
    deleted_at timestamptz,
    attempt_count integer NOT NULL DEFAULT 0,
    locked_at timestamptz,
    locked_by text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT artwork_revision_gc_original_path_check CHECK (BTRIM(original_path) <> ''),
    CONSTRAINT artwork_revision_gc_manifest_check CHECK (cardinality(object_keys) > 0 OR BTRIM(image_type) <> ''),
    CONSTRAINT artwork_revision_gc_attempt_count_check CHECK (attempt_count >= 0)
);

CREATE INDEX artwork_revision_gc_due_idx
    ON public.artwork_revision_gc_candidates (next_attempt_at, id)
    WHERE locked_at IS NULL AND next_attempt_at IS NOT NULL;

CREATE INDEX artwork_revision_gc_lease_idx
    ON public.artwork_revision_gc_candidates (locked_at, id)
    WHERE locked_at IS NOT NULL;

-- The dormant sweep periodically re-verifies parked rows so a reference that
-- disappears through a surface without a displacement trigger degrades to
-- slower cleanup instead of a permanent leak.
CREATE INDEX artwork_revision_gc_dormant_idx
    ON public.artwork_revision_gc_candidates (updated_at, id)
    WHERE next_attempt_at IS NULL;

-- Queue displaced immutable artwork at the database boundary so every writer
-- participates in the same lifecycle, including background refreshes, scanners,
-- localizations, and future code paths that do not use the admin publication API.
-- The trigger stores only the displaced path and its image type; the collector
-- expands object keys in Go so the variant ladder has a single source of truth.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.queue_displaced_artwork_revision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    old_row jsonb := to_jsonb(OLD);
    new_row jsonb;
    arg_index integer := 0;
    path_column text;
    image_type text;
    previous_path text;
    replacement_path text;
    cleanup_at timestamptz := NOW() + interval '24 hours';
BEGIN
    IF TG_OP = 'UPDATE' THEN
        new_row := to_jsonb(NEW);
    END IF;

    WHILE arg_index < TG_NARGS LOOP
        path_column := TG_ARGV[arg_index];
        image_type := TG_ARGV[arg_index + 1];
        previous_path := old_row ->> path_column;
        replacement_path := CASE WHEN new_row IS NULL THEN NULL ELSE new_row ->> path_column END;

        IF COALESCE(BTRIM(previous_path), '') <> ''
           AND previous_path NOT LIKE '%://%'
           AND previous_path ~ '/original\.[^/]+$'
           AND (TG_OP = 'DELETE' OR previous_path IS DISTINCT FROM replacement_path) THEN
            INSERT INTO public.artwork_revision_gc_candidates (
                original_path, image_type, object_keys, not_before, next_attempt_at
            ) VALUES (previous_path, image_type, '{}', cleanup_at, cleanup_at)
            ON CONFLICT (original_path) DO UPDATE SET
                image_type = CASE
                    WHEN artwork_revision_gc_candidates.image_type = '' THEN EXCLUDED.image_type
                    ELSE artwork_revision_gc_candidates.image_type
                END,
                not_before = EXCLUDED.not_before,
                next_attempt_at = EXCLUDED.next_attempt_at,
                attempt_count = 0,
                locked_at = NULL,
                locked_by = '',
                last_error = '',
                updated_at = NOW();
        END IF;

        arg_index := arg_index + 2;
    END LOOP;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- UPDATE triggers carry WHEN clauses so the function only runs when an artwork
-- column actually changes; catalog upserts assign these columns on every row
-- write. DELETE triggers cannot reference NEW and are declared separately.
CREATE TRIGGER media_items_artwork_revision_gc_update
AFTER UPDATE OF poster_path, backdrop_path, logo_path ON public.media_items
FOR EACH ROW
WHEN (OLD.poster_path IS DISTINCT FROM NEW.poster_path
   OR OLD.backdrop_path IS DISTINCT FROM NEW.backdrop_path
   OR OLD.logo_path IS DISTINCT FROM NEW.logo_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop', 'logo_path', 'logo'
);

CREATE TRIGGER media_items_artwork_revision_gc_delete
AFTER DELETE ON public.media_items
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop', 'logo_path', 'logo'
);

CREATE TRIGGER media_item_localizations_artwork_revision_gc_update
AFTER UPDATE OF poster_path, backdrop_path, logo_path ON public.media_item_localizations
FOR EACH ROW
WHEN (OLD.poster_path IS DISTINCT FROM NEW.poster_path
   OR OLD.backdrop_path IS DISTINCT FROM NEW.backdrop_path
   OR OLD.logo_path IS DISTINCT FROM NEW.logo_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop', 'logo_path', 'logo'
);

CREATE TRIGGER media_item_localizations_artwork_revision_gc_delete
AFTER DELETE ON public.media_item_localizations
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop', 'logo_path', 'logo'
);

CREATE TRIGGER seasons_artwork_revision_gc_update
AFTER UPDATE OF poster_path ON public.seasons
FOR EACH ROW
WHEN (OLD.poster_path IS DISTINCT FROM NEW.poster_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision('poster_path', 'poster');

CREATE TRIGGER seasons_artwork_revision_gc_delete
AFTER DELETE ON public.seasons
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision('poster_path', 'poster');

CREATE TRIGGER season_localizations_artwork_revision_gc_update
AFTER UPDATE OF poster_path ON public.season_localizations
FOR EACH ROW
WHEN (OLD.poster_path IS DISTINCT FROM NEW.poster_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision('poster_path', 'poster');

CREATE TRIGGER season_localizations_artwork_revision_gc_delete
AFTER DELETE ON public.season_localizations
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision('poster_path', 'poster');

CREATE TRIGGER episodes_artwork_revision_gc_update
AFTER UPDATE OF still_path ON public.episodes
FOR EACH ROW
WHEN (OLD.still_path IS DISTINCT FROM NEW.still_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision('still_path', 'still');

CREATE TRIGGER episodes_artwork_revision_gc_delete
AFTER DELETE ON public.episodes
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision('still_path', 'still');

CREATE TRIGGER people_artwork_revision_gc_update
AFTER UPDATE OF photo_path ON public.people
FOR EACH ROW
WHEN (OLD.photo_path IS DISTINCT FROM NEW.photo_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision('photo_path', 'profile');

CREATE TRIGGER people_artwork_revision_gc_delete
AFTER DELETE ON public.people
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision('photo_path', 'profile');

-- +goose Down
DROP TRIGGER IF EXISTS people_artwork_revision_gc_delete ON public.people;
DROP TRIGGER IF EXISTS people_artwork_revision_gc_update ON public.people;
DROP TRIGGER IF EXISTS episodes_artwork_revision_gc_delete ON public.episodes;
DROP TRIGGER IF EXISTS episodes_artwork_revision_gc_update ON public.episodes;
DROP TRIGGER IF EXISTS season_localizations_artwork_revision_gc_delete ON public.season_localizations;
DROP TRIGGER IF EXISTS season_localizations_artwork_revision_gc_update ON public.season_localizations;
DROP TRIGGER IF EXISTS seasons_artwork_revision_gc_delete ON public.seasons;
DROP TRIGGER IF EXISTS seasons_artwork_revision_gc_update ON public.seasons;
DROP TRIGGER IF EXISTS media_item_localizations_artwork_revision_gc_delete ON public.media_item_localizations;
DROP TRIGGER IF EXISTS media_item_localizations_artwork_revision_gc_update ON public.media_item_localizations;
DROP TRIGGER IF EXISTS media_items_artwork_revision_gc_delete ON public.media_items;
DROP TRIGGER IF EXISTS media_items_artwork_revision_gc_update ON public.media_items;
DROP FUNCTION IF EXISTS public.queue_displaced_artwork_revision();
DROP TABLE IF EXISTS public.artwork_revision_gc_candidates;
