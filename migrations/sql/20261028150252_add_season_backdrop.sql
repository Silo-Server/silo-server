-- +goose Up
-- A season's landscape art, following its poster columns: the served path,
-- the provider source the image cache resolves, and a thumbhash. Continue
-- Watching and Next Up show it on episode cards before the series backdrop.
ALTER TABLE public.seasons
    ADD COLUMN IF NOT EXISTS backdrop_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS backdrop_source_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS backdrop_thumbhash text NOT NULL DEFAULT '';

DROP TRIGGER IF EXISTS seasons_artwork_revision_gc_update ON public.seasons;
DROP TRIGGER IF EXISTS seasons_artwork_revision_gc_delete ON public.seasons;

CREATE TRIGGER seasons_artwork_revision_gc_update
AFTER UPDATE OF poster_path, backdrop_path ON public.seasons
FOR EACH ROW
WHEN (OLD.poster_path IS DISTINCT FROM NEW.poster_path
   OR OLD.backdrop_path IS DISTINCT FROM NEW.backdrop_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop'
);

CREATE TRIGGER seasons_artwork_revision_gc_delete
AFTER DELETE ON public.seasons
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop'
);

-- A season backdrop published without the current ladder's widest rung
-- reopens a finished ladder backfill, as the other backdrop surfaces do.
DROP TRIGGER IF EXISTS seasons_reopen_image_ladder ON public.seasons;

CREATE TRIGGER seasons_reopen_image_ladder
AFTER INSERT OR UPDATE OF poster_path, backdrop_path ON public.seasons
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '3',
    'poster_path', 'poster', '%/w1000.%',
    'backdrop_path', 'backdrop', '%/w2560.%'
);

-- Season jobs may cache the backdrop too, so the image cache can fetch a
-- provider season backdrop again after reconcile resets a missing copy to its
-- source. The check only widens, so every existing row passes it: it is added
-- NOT VALID and the next migration validates it without blocking writers.
ALTER TABLE public.metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_shape_check;

ALTER TABLE public.metadata_image_cache_jobs
    ADD CONSTRAINT metadata_image_cache_jobs_shape_check CHECK (
        (
            target_type = 'item'
            AND image_type IN ('poster', 'backdrop', 'logo', 'banner', 'thumb', 'clearart', 'disc')
            AND target_language = ''
            AND season_number IS NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'item_localization'
            AND image_type IN ('poster', 'backdrop', 'logo')
            AND target_language <> ''
            AND season_number IS NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'item_backdrop'
            AND image_type = 'backdrop'
            AND target_language = ''
            AND season_number IS NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'season'
            AND image_type IN ('poster', 'backdrop')
            AND target_language = ''
            AND season_number IS NOT NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'season_localization'
            AND image_type = 'poster'
            AND target_language <> ''
            AND season_number IS NOT NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'episode'
            AND image_type = 'still'
            AND target_language = ''
            AND season_number IS NOT NULL
            AND episode_number IS NOT NULL
        )
        OR (
            target_type = 'person'
            AND image_type = 'profile'
            AND target_language = ''
            AND season_number IS NULL
            AND episode_number IS NULL
        )
    ) NOT VALID;

-- +goose Down
DELETE FROM public.metadata_image_cache_jobs
WHERE target_type = 'season' AND image_type = 'backdrop';

ALTER TABLE public.metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_shape_check;

ALTER TABLE public.metadata_image_cache_jobs
    ADD CONSTRAINT metadata_image_cache_jobs_shape_check CHECK (
        (
            target_type = 'item'
            AND image_type IN ('poster', 'backdrop', 'logo', 'banner', 'thumb', 'clearart', 'disc')
            AND target_language = ''
            AND season_number IS NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'item_localization'
            AND image_type IN ('poster', 'backdrop', 'logo')
            AND target_language <> ''
            AND season_number IS NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'item_backdrop'
            AND image_type = 'backdrop'
            AND target_language = ''
            AND season_number IS NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'season'
            AND image_type = 'poster'
            AND target_language = ''
            AND season_number IS NOT NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'season_localization'
            AND image_type = 'poster'
            AND target_language <> ''
            AND season_number IS NOT NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'episode'
            AND image_type = 'still'
            AND target_language = ''
            AND season_number IS NOT NULL
            AND episode_number IS NOT NULL
        )
        OR (
            target_type = 'person'
            AND image_type = 'profile'
            AND target_language = ''
            AND season_number IS NULL
            AND episode_number IS NULL
        )
    ) NOT VALID;

-- The narrower check is added NOT VALID too, so a rollback does not scan the
-- job queue while holding the lock that blocks its writers. It still holds
-- for every new row, and every row left after the delete above passes it.

DROP TRIGGER IF EXISTS seasons_reopen_image_ladder ON public.seasons;

CREATE TRIGGER seasons_reopen_image_ladder
AFTER INSERT OR UPDATE OF poster_path ON public.seasons
FOR EACH ROW EXECUTE FUNCTION public.reopen_image_ladder_backfill(
    '3',
    'poster_path', 'poster', '%/w1000.%'
);

DROP TRIGGER IF EXISTS seasons_artwork_revision_gc_update ON public.seasons;
DROP TRIGGER IF EXISTS seasons_artwork_revision_gc_delete ON public.seasons;

CREATE TRIGGER seasons_artwork_revision_gc_update
AFTER UPDATE OF poster_path ON public.seasons
FOR EACH ROW
WHEN (OLD.poster_path IS DISTINCT FROM NEW.poster_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision('poster_path', 'poster');

CREATE TRIGGER seasons_artwork_revision_gc_delete
AFTER DELETE ON public.seasons
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision('poster_path', 'poster');

ALTER TABLE public.seasons
    DROP COLUMN IF EXISTS backdrop_thumbhash,
    DROP COLUMN IF EXISTS backdrop_source_path,
    DROP COLUMN IF EXISTS backdrop_path;
