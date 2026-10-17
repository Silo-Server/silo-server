-- +goose Up
-- Ladder v3 widens posters to w1000, stills to w1280 and backdrops to w2560.
-- The v2 fence reopened the backfill whenever a published poster or still
-- lacked w780, which every v3 revision does, so each new image would have
-- reset a finished backfill. Replace it with one function that takes the
-- ladder version it fences and the rung each image type gained, and cover the
-- backdrop surfaces the v3 backfill regenerates.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.reopen_image_ladder_backfill()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    new_row jsonb := to_jsonb(NEW);
    old_row jsonb;
    fence_version integer := TG_ARGV[0]::integer;
    arg_index integer := 1;
    path_column text;
    manifest_image_type text;
    rung_pattern text;
    cached_path text;
    previous_path text;
    state_version integer;
    changed_cached_path boolean := false;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_row := to_jsonb(OLD);
    END IF;

    -- Arguments after the version are (path column, image type, rung pattern)
    -- triplets. Find an affected local cached-path publication before taking
    -- the shared singleton lock. The lock is still taken even when the state is
    -- below the fenced version: that closes the ordering with a concurrent final
    -- confirmation.
    WHILE arg_index < TG_NARGS LOOP
        path_column := TG_ARGV[arg_index];
        cached_path := new_row ->> path_column;
        previous_path := CASE WHEN old_row IS NULL THEN NULL ELSE old_row ->> path_column END;
        IF COALESCE(BTRIM(cached_path), '') <> ''
           AND cached_path NOT LIKE '%://%'
           AND (TG_OP = 'INSERT' OR cached_path IS DISTINCT FROM previous_path) THEN
            changed_cached_path := true;
            EXIT;
        END IF;
        arg_index := arg_index + 3;
    END LOOP;

    IF NOT changed_cached_path THEN
        RETURN NEW;
    END IF;

    SELECT backfilled_version
    INTO state_version
    FROM public.image_ladder_backfill_state
    WHERE id = 1
    FOR UPDATE;

    IF state_version < fence_version THEN
        RETURN NEW;
    END IF;

    arg_index := 1;
    WHILE arg_index < TG_NARGS LOOP
        path_column := TG_ARGV[arg_index];
        manifest_image_type := TG_ARGV[arg_index + 1];
        rung_pattern := TG_ARGV[arg_index + 2];
        cached_path := new_row ->> path_column;
        previous_path := CASE WHEN old_row IS NULL THEN NULL ELSE old_row ->> path_column END;

        IF COALESCE(BTRIM(cached_path), '') <> ''
           AND cached_path NOT LIKE '%://%'
           AND (TG_OP = 'INSERT' OR cached_path IS DISTINCT FROM previous_path)
           AND NOT EXISTS (
               SELECT 1
               FROM public.artwork_revision_gc_candidates manifest
               WHERE manifest.original_path = cached_path
                 AND manifest.image_type = manifest_image_type
                 AND EXISTS (
                     SELECT 1
                     FROM unnest(manifest.object_keys) object_key
                     WHERE object_key LIKE rung_pattern
                 )
           ) THEN
            UPDATE public.image_ladder_backfill_state
            SET backfilled_version = LEAST(backfilled_version, fence_version - 1),
                last_attempt_at = NULL,
                updated_at = NOW()
            WHERE id = 1;
            RETURN NEW;
        END IF;

        arg_index := arg_index + 3;
    END LOOP;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS episodes_reopen_image_ladder_v2 ON public.episodes;
DROP TRIGGER IF EXISTS season_localizations_reopen_image_ladder_v2 ON public.season_localizations;
DROP TRIGGER IF EXISTS seasons_reopen_image_ladder_v2 ON public.seasons;
DROP TRIGGER IF EXISTS media_item_localizations_reopen_image_ladder_v2 ON public.media_item_localizations;
DROP TRIGGER IF EXISTS media_items_reopen_image_ladder_v2 ON public.media_items;
DROP FUNCTION IF EXISTS public.reopen_image_ladder_backfill_v2();

CREATE TRIGGER media_items_reopen_image_ladder
AFTER INSERT OR UPDATE OF poster_path, logo_path, backdrop_path ON public.media_items
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '3',
    'poster_path', 'poster', '%/w1000.%',
    'logo_path', 'logo', '%/w1280.%',
    'backdrop_path', 'backdrop', '%/w2560.%'
);

CREATE TRIGGER media_item_localizations_reopen_image_ladder
AFTER INSERT OR UPDATE OF poster_path, logo_path, backdrop_path ON public.media_item_localizations
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '3',
    'poster_path', 'poster', '%/w1000.%',
    'logo_path', 'logo', '%/w1280.%',
    'backdrop_path', 'backdrop', '%/w2560.%'
);

CREATE TRIGGER media_item_backdrops_reopen_image_ladder
AFTER INSERT OR UPDATE OF path ON public.media_item_backdrops
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '3',
    'path', 'backdrop', '%/w2560.%'
);

CREATE TRIGGER seasons_reopen_image_ladder
AFTER INSERT OR UPDATE OF poster_path ON public.seasons
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '3',
    'poster_path', 'poster', '%/w1000.%'
);

CREATE TRIGGER season_localizations_reopen_image_ladder
AFTER INSERT OR UPDATE OF poster_path ON public.season_localizations
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '3',
    'poster_path', 'poster', '%/w1000.%'
);

CREATE TRIGGER episodes_reopen_image_ladder
AFTER INSERT OR UPDATE OF still_path ON public.episodes
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '3',
    'still_path', 'still', '%/w1280.%'
);

-- +goose Down
DROP TRIGGER IF EXISTS episodes_reopen_image_ladder ON public.episodes;
DROP TRIGGER IF EXISTS season_localizations_reopen_image_ladder ON public.season_localizations;
DROP TRIGGER IF EXISTS seasons_reopen_image_ladder ON public.seasons;
DROP TRIGGER IF EXISTS media_item_backdrops_reopen_image_ladder ON public.media_item_backdrops;
DROP TRIGGER IF EXISTS media_item_localizations_reopen_image_ladder ON public.media_item_localizations;
DROP TRIGGER IF EXISTS media_items_reopen_image_ladder ON public.media_items;

-- Restore the v2 fence (ladder v2 rungs, no backdrops) on the shared function.
CREATE TRIGGER media_items_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF poster_path, logo_path ON public.media_items
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '2',
    'poster_path', 'poster', '%/w780.%',
    'logo_path', 'logo', '%/w1280.%'
);

CREATE TRIGGER media_item_localizations_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF poster_path, logo_path ON public.media_item_localizations
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '2',
    'poster_path', 'poster', '%/w780.%',
    'logo_path', 'logo', '%/w1280.%'
);

CREATE TRIGGER seasons_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF poster_path ON public.seasons
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '2',
    'poster_path', 'poster', '%/w780.%'
);

CREATE TRIGGER season_localizations_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF poster_path ON public.season_localizations
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '2',
    'poster_path', 'poster', '%/w780.%'
);

CREATE TRIGGER episodes_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF still_path ON public.episodes
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '2',
    'still_path', 'still', '%/w780.%'
);
