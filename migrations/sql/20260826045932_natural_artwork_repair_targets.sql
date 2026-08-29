-- +goose Up
ALTER TABLE public.metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_target_unique;

UPDATE public.metadata_image_cache_jobs
SET target_content_id = series_id
WHERE target_type IN ('season', 'season_localization', 'episode')
  AND series_id <> '';

WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY target_type, target_content_id, image_type,
                            target_language, season_number, episode_number
               ORDER BY repair_requested DESC, updated_at DESC, id DESC
           ) AS position
    FROM public.metadata_image_cache_jobs
)
DELETE FROM public.metadata_image_cache_jobs j
USING ranked r
WHERE j.id = r.id
  AND r.position > 1;

ALTER TABLE public.metadata_image_cache_jobs
    ADD CONSTRAINT metadata_image_cache_jobs_target_unique
        UNIQUE NULLS NOT DISTINCT (
            target_type,
            target_content_id,
            image_type,
            target_language,
            season_number,
            episode_number
        );

-- +goose Down
ALTER TABLE public.metadata_image_cache_jobs
    DROP CONSTRAINT IF EXISTS metadata_image_cache_jobs_target_unique;

DELETE FROM public.metadata_image_cache_jobs j
WHERE (j.target_type IN ('season', 'season_localization') AND NOT EXISTS (
        SELECT 1 FROM public.seasons s
        WHERE s.series_id = j.target_content_id
          AND s.season_number = j.season_number
    ))
   OR (j.target_type = 'episode' AND NOT EXISTS (
        SELECT 1 FROM public.episodes e
        WHERE e.series_id = j.target_content_id
          AND e.season_number = j.season_number
          AND e.episode_number = j.episode_number
    ));

UPDATE public.metadata_image_cache_jobs j
SET target_content_id = CASE
    WHEN j.target_type IN ('season', 'season_localization') THEN (
        SELECT s.content_id FROM public.seasons s
        WHERE s.series_id = j.target_content_id
          AND s.season_number = j.season_number
    )
    WHEN j.target_type = 'episode' THEN (
        SELECT e.content_id FROM public.episodes e
        WHERE e.series_id = j.target_content_id
          AND e.season_number = j.season_number
          AND e.episode_number = j.episode_number
    )
    ELSE j.target_content_id
END
WHERE j.target_type IN ('season', 'season_localization', 'episode');

ALTER TABLE public.metadata_image_cache_jobs
    ADD CONSTRAINT metadata_image_cache_jobs_target_unique
        UNIQUE (target_type, target_content_id, image_type, target_language);
