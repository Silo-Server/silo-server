-- +goose Up
ALTER TABLE public.seasons
    ADD COLUMN IF NOT EXISTS poster_source_path text NOT NULL DEFAULT '';

ALTER TABLE public.episodes
    ADD COLUMN IF NOT EXISTS still_source_path text NOT NULL DEFAULT '';

UPDATE public.seasons
SET poster_source_path = poster_path
WHERE poster_source_path = ''
  AND poster_path LIKE '%://%'
  AND lower(poster_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

UPDATE public.episodes
SET still_source_path = still_path
WHERE still_source_path = ''
  AND still_path LIKE '%://%'
  AND lower(still_path) NOT LIKE ALL (ARRAY['s3://%', 'file://%', 'local://%', 'upload://%', 'generated://%']);

CREATE TABLE public.metadata_image_cache_jobs (
    id bigserial PRIMARY KEY,
    target_type text NOT NULL,
    target_content_id text NOT NULL,
    series_id text NOT NULL,
    source_path text NOT NULL,
    provider_id text NOT NULL,
    provider_content_id text NOT NULL,
    content_type text NOT NULL DEFAULT 'series',
    image_type text NOT NULL,
    season_number integer,
    episode_number integer,
    status text NOT NULL DEFAULT 'queued',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamp with time zone NOT NULL DEFAULT now(),
    locked_at timestamp with time zone,
    locked_by text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    completed_at timestamp with time zone,
    CONSTRAINT metadata_image_cache_jobs_target_check
        CHECK (target_type IN ('season', 'episode')),
    CONSTRAINT metadata_image_cache_jobs_image_type_check
        CHECK (image_type IN ('poster', 'still')),
    CONSTRAINT metadata_image_cache_jobs_status_check
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    CONSTRAINT metadata_image_cache_jobs_shape_check CHECK (
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
    CONSTRAINT metadata_image_cache_jobs_target_unique
        UNIQUE (target_type, target_content_id, image_type)
);

CREATE INDEX metadata_image_cache_jobs_due_idx
    ON public.metadata_image_cache_jobs (next_attempt_at, id)
    WHERE status = 'queued';

CREATE INDEX metadata_image_cache_jobs_running_lease_idx
    ON public.metadata_image_cache_jobs (locked_at, id)
    WHERE status = 'running';

CREATE INDEX metadata_image_cache_jobs_series_idx
    ON public.metadata_image_cache_jobs (series_id, status);

CREATE INDEX metadata_image_cache_jobs_succeeded_retention_idx
    ON public.metadata_image_cache_jobs (completed_at, id)
    WHERE status = 'succeeded';

-- +goose Down
DROP TABLE IF EXISTS public.metadata_image_cache_jobs;

ALTER TABLE public.episodes
    DROP COLUMN IF EXISTS still_source_path;

ALTER TABLE public.seasons
    DROP COLUMN IF EXISTS poster_source_path;
