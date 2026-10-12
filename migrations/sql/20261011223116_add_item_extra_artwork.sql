-- +goose Up
-- Banner, thumb (landscape), clear art and disc art follow the poster,
-- backdrop and logo columns: a served path, the provider or file:// source the
-- image cache resolves, and a thumbhash for the photographic types.
ALTER TABLE public.media_items
    ADD COLUMN IF NOT EXISTS banner_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS banner_source_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS banner_thumbhash text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS thumb_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS thumb_source_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS thumb_thumbhash text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS clearart_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS clearart_source_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS disc_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS disc_source_path text NOT NULL DEFAULT '';

-- Backdrops after the first. media_items.backdrop_* stays the primary
-- backdrop, so ordinals here start at 1 and every reader of the primary is
-- unchanged.
CREATE TABLE public.media_item_backdrops (
    id bigserial PRIMARY KEY,
    content_id text NOT NULL,
    ordinal integer NOT NULL,
    path text NOT NULL DEFAULT '',
    source_path text NOT NULL DEFAULT '',
    thumbhash text NOT NULL DEFAULT '',
    CONSTRAINT media_item_backdrops_content_id_fkey FOREIGN KEY (content_id)
        REFERENCES public.media_items(content_id) ON UPDATE CASCADE ON DELETE CASCADE,
    CONSTRAINT media_item_backdrops_ordinal_check CHECK (ordinal >= 1),
    CONSTRAINT media_item_backdrops_content_ordinal_key UNIQUE (content_id, ordinal)
);

-- The artwork revision GC checks references by path on every sweep surface.
-- A row's path stays '' until its image is cached.
CREATE INDEX idx_media_item_backdrops_path_gc
    ON public.media_item_backdrops (path) WHERE path <> '';

DROP TRIGGER IF EXISTS media_items_artwork_revision_gc_update ON public.media_items;
DROP TRIGGER IF EXISTS media_items_artwork_revision_gc_delete ON public.media_items;

CREATE TRIGGER media_items_artwork_revision_gc_update
AFTER UPDATE OF poster_path, backdrop_path, logo_path, banner_path, thumb_path, clearart_path, disc_path ON public.media_items
FOR EACH ROW
WHEN (OLD.poster_path IS DISTINCT FROM NEW.poster_path
   OR OLD.backdrop_path IS DISTINCT FROM NEW.backdrop_path
   OR OLD.logo_path IS DISTINCT FROM NEW.logo_path
   OR OLD.banner_path IS DISTINCT FROM NEW.banner_path
   OR OLD.thumb_path IS DISTINCT FROM NEW.thumb_path
   OR OLD.clearart_path IS DISTINCT FROM NEW.clearart_path
   OR OLD.disc_path IS DISTINCT FROM NEW.disc_path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop', 'logo_path', 'logo',
    'banner_path', 'banner', 'thumb_path', 'thumb', 'clearart_path', 'clearart', 'disc_path', 'disc'
);

CREATE TRIGGER media_items_artwork_revision_gc_delete
AFTER DELETE ON public.media_items
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision(
    'poster_path', 'poster', 'backdrop_path', 'backdrop', 'logo_path', 'logo',
    'banner_path', 'banner', 'thumb_path', 'thumb', 'clearart_path', 'clearart', 'disc_path', 'disc'
);

CREATE TRIGGER media_item_backdrops_artwork_revision_gc_update
AFTER UPDATE OF path ON public.media_item_backdrops
FOR EACH ROW
WHEN (OLD.path IS DISTINCT FROM NEW.path)
EXECUTE FUNCTION public.queue_displaced_artwork_revision('path', 'backdrop');

CREATE TRIGGER media_item_backdrops_artwork_revision_gc_delete
AFTER DELETE ON public.media_item_backdrops
FOR EACH ROW EXECUTE FUNCTION public.queue_displaced_artwork_revision('path', 'backdrop');

-- Item jobs may cache the new slots. An item_backdrop job targets one
-- media_item_backdrops row by id and carries the item's content ID in
-- series_id, as a person job targets people.id. The checks only widen, so
-- every existing row passes them: they are added NOT VALID to skip the scan
-- while this transaction holds media_items, and the next migration validates
-- them without blocking writers.
ALTER TABLE public.metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_target_check,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_image_type_check,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_shape_check;

ALTER TABLE public.metadata_image_cache_jobs
    ADD CONSTRAINT metadata_image_cache_jobs_target_check
        CHECK (target_type IN ('item', 'item_localization', 'item_backdrop', 'season', 'season_localization', 'episode', 'person')) NOT VALID,
    ADD CONSTRAINT metadata_image_cache_jobs_image_type_check
        CHECK (image_type IN ('poster', 'backdrop', 'logo', 'banner', 'thumb', 'clearart', 'disc', 'still', 'profile')) NOT VALID,
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

-- +goose Down
DELETE FROM public.metadata_image_cache_jobs
WHERE target_type = 'item_backdrop'
   OR image_type IN ('banner', 'thumb', 'clearart', 'disc');

ALTER TABLE public.metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_target_check,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_image_type_check,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_shape_check;

ALTER TABLE public.metadata_image_cache_jobs
    ADD CONSTRAINT metadata_image_cache_jobs_target_check
        CHECK (target_type IN ('item', 'item_localization', 'season', 'season_localization', 'episode', 'person')),
    ADD CONSTRAINT metadata_image_cache_jobs_image_type_check
        CHECK (image_type IN ('poster', 'backdrop', 'logo', 'still', 'profile')),
    ADD CONSTRAINT metadata_image_cache_jobs_shape_check CHECK (
        (
            target_type = 'item'
            AND image_type IN ('poster', 'backdrop', 'logo')
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
    );

DROP TRIGGER IF EXISTS media_items_artwork_revision_gc_update ON public.media_items;
DROP TRIGGER IF EXISTS media_items_artwork_revision_gc_delete ON public.media_items;

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

DROP TABLE IF EXISTS public.media_item_backdrops;

ALTER TABLE public.media_items
    DROP COLUMN IF EXISTS disc_source_path,
    DROP COLUMN IF EXISTS disc_path,
    DROP COLUMN IF EXISTS clearart_source_path,
    DROP COLUMN IF EXISTS clearart_path,
    DROP COLUMN IF EXISTS thumb_thumbhash,
    DROP COLUMN IF EXISTS thumb_source_path,
    DROP COLUMN IF EXISTS thumb_path,
    DROP COLUMN IF EXISTS banner_thumbhash,
    DROP COLUMN IF EXISTS banner_source_path,
    DROP COLUMN IF EXISTS banner_path;
