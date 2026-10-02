-- +goose Up
-- A rolling upgrade can leave an old image-cache worker running after a new
-- worker has proved ladder v2 complete. If the old worker publishes afterward,
-- its immutable revision lacks the new rung. Reopen the singleton state at the
-- database boundary so even old binaries participate in the fence.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.reopen_image_ladder_backfill_v2()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    new_row jsonb := to_jsonb(NEW);
    old_row jsonb;
    arg_index integer := 0;
    path_column text;
    image_type text;
    rung_pattern text;
    cached_path text;
    previous_path text;
    state_version integer;
    changed_cached_path boolean := false;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        old_row := to_jsonb(OLD);
    END IF;

    -- Find an affected local cached-path publication before taking the shared
    -- singleton lock. The lock is still taken even when the state is below v2:
    -- that closes the ordering with a concurrent final confirmation.
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

    IF state_version < 2 THEN
        RETURN NEW;
    END IF;

    arg_index := 0;
    WHILE arg_index < TG_NARGS LOOP
        path_column := TG_ARGV[arg_index];
        image_type := TG_ARGV[arg_index + 1];
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
                 AND manifest.image_type = image_type
                 AND EXISTS (
                     SELECT 1
                     FROM unnest(manifest.object_keys) object_key
                     WHERE object_key LIKE rung_pattern
                 )
           ) THEN
            UPDATE public.image_ladder_backfill_state
            SET backfilled_version = LEAST(backfilled_version, 1),
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

CREATE TRIGGER media_items_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF poster_path, logo_path ON public.media_items
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill_v2(
    'poster_path', 'poster', '%/w780.%',
    'logo_path', 'logo', '%/w1280.%'
);

CREATE TRIGGER media_item_localizations_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF poster_path, logo_path ON public.media_item_localizations
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill_v2(
    'poster_path', 'poster', '%/w780.%',
    'logo_path', 'logo', '%/w1280.%'
);

CREATE TRIGGER seasons_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF poster_path ON public.seasons
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill_v2(
    'poster_path', 'poster', '%/w780.%'
);

CREATE TRIGGER season_localizations_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF poster_path ON public.season_localizations
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill_v2(
    'poster_path', 'poster', '%/w780.%'
);

CREATE TRIGGER episodes_reopen_image_ladder_v2
AFTER INSERT OR UPDATE OF still_path ON public.episodes
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill_v2(
    'still_path', 'still', '%/w780.%'
);

-- The PR was already exercised on shared development before the upload fence
-- existed, so an exact manifest containing a new key is not proof that upload
-- succeeded. Invalidate those manifests once and reopen v2; the normal sweep
-- regenerates them and replaces the pending manifest only after all uploads
-- succeed.
UPDATE public.artwork_revision_gc_candidates
SET object_keys = '{}',
    updated_at = NOW()
WHERE image_type IN ('poster', 'still', 'logo');

UPDATE public.image_ladder_backfill_state
SET backfilled_version = LEAST(backfilled_version, 1),
    last_attempt_at = NULL,
    updated_at = NOW()
WHERE id = 1;

-- +goose Down
DROP TRIGGER IF EXISTS episodes_reopen_image_ladder_v2 ON public.episodes;
DROP TRIGGER IF EXISTS season_localizations_reopen_image_ladder_v2 ON public.season_localizations;
DROP TRIGGER IF EXISTS seasons_reopen_image_ladder_v2 ON public.seasons;
DROP TRIGGER IF EXISTS media_item_localizations_reopen_image_ladder_v2 ON public.media_item_localizations;
DROP TRIGGER IF EXISTS media_items_reopen_image_ladder_v2 ON public.media_items;
DROP FUNCTION IF EXISTS public.reopen_image_ladder_backfill_v2();
