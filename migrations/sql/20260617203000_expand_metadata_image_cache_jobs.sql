-- +goose Up
ALTER TABLE public.media_items
    ALTER COLUMN poster_source_path SET DEFAULT '';

UPDATE public.media_items
SET poster_source_path = ''
WHERE poster_source_path IS NULL;

ALTER TABLE public.media_items
    ALTER COLUMN poster_source_path SET NOT NULL,
    ADD COLUMN IF NOT EXISTS backdrop_source_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS logo_source_path text NOT NULL DEFAULT '';

ALTER TABLE public.people
    ADD COLUMN IF NOT EXISTS photo_source_path text NOT NULL DEFAULT '';

ALTER TABLE public.media_item_localizations
    ADD COLUMN IF NOT EXISTS poster_source_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS backdrop_source_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS logo_source_path text NOT NULL DEFAULT '';

ALTER TABLE public.season_localizations
    ADD COLUMN IF NOT EXISTS poster_source_path text NOT NULL DEFAULT '';

UPDATE public.media_items
SET poster_source_path = poster_path
WHERE poster_source_path = ''
  AND poster_path LIKE '%://%'
  AND lower(poster_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

UPDATE public.media_items
SET backdrop_source_path = backdrop_path
WHERE backdrop_source_path = ''
  AND backdrop_path LIKE '%://%'
  AND lower(backdrop_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

UPDATE public.media_items
SET logo_source_path = logo_path
WHERE logo_source_path = ''
  AND logo_path LIKE '%://%'
  AND lower(logo_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

UPDATE public.people
SET photo_source_path = photo_path
WHERE photo_source_path = ''
  AND photo_path LIKE '%://%'
  AND lower(photo_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

UPDATE public.media_item_localizations
SET poster_source_path = poster_path
WHERE poster_source_path = ''
  AND poster_path LIKE '%://%'
  AND lower(poster_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

UPDATE public.media_item_localizations
SET backdrop_source_path = backdrop_path
WHERE backdrop_source_path = ''
  AND backdrop_path LIKE '%://%'
  AND lower(backdrop_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

UPDATE public.media_item_localizations
SET logo_source_path = logo_path
WHERE logo_source_path = ''
  AND logo_path LIKE '%://%'
  AND lower(logo_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

UPDATE public.season_localizations
SET poster_source_path = poster_path
WHERE poster_source_path = ''
  AND poster_path LIKE '%://%'
  AND lower(poster_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

ALTER TABLE public.metadata_image_cache_jobs
    ADD COLUMN IF NOT EXISTS target_language text NOT NULL DEFAULT '';

ALTER TABLE public.metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_target_unique,
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
    ),
    ADD CONSTRAINT metadata_image_cache_jobs_target_unique
        UNIQUE (target_type, target_content_id, image_type, target_language);

-- +goose Down
DELETE FROM public.metadata_image_cache_jobs
WHERE target_type NOT IN ('season', 'episode')
   OR image_type NOT IN ('poster', 'still')
   OR target_language <> '';

ALTER TABLE public.metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_target_unique,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_target_check,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_image_type_check,
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_shape_check;

ALTER TABLE public.metadata_image_cache_jobs
    ADD CONSTRAINT metadata_image_cache_jobs_target_check
        CHECK (target_type IN ('season', 'episode')),
    ADD CONSTRAINT metadata_image_cache_jobs_image_type_check
        CHECK (image_type IN ('poster', 'still')),
    ADD CONSTRAINT metadata_image_cache_jobs_shape_check CHECK (
        (
            target_type = 'season'
            AND image_type = 'poster'
            AND season_number IS NOT NULL
            AND episode_number IS NULL
        )
        OR (
            target_type = 'episode'
            AND image_type = 'still'
            AND season_number IS NOT NULL
            AND episode_number IS NOT NULL
        )
    ),
    ADD CONSTRAINT metadata_image_cache_jobs_target_unique
        UNIQUE (target_type, target_content_id, image_type);

ALTER TABLE public.metadata_image_cache_jobs
    DROP COLUMN IF EXISTS target_language;

ALTER TABLE public.season_localizations
    DROP COLUMN IF EXISTS poster_source_path;

ALTER TABLE public.media_item_localizations
    DROP COLUMN IF EXISTS logo_source_path,
    DROP COLUMN IF EXISTS backdrop_source_path,
    DROP COLUMN IF EXISTS poster_source_path;

ALTER TABLE public.people
    DROP COLUMN IF EXISTS photo_source_path;

ALTER TABLE public.media_items
    DROP COLUMN IF EXISTS logo_source_path,
    DROP COLUMN IF EXISTS backdrop_source_path;
