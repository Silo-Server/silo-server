-- +goose NO TRANSACTION

-- +goose Up
-- A movie's earliest home releases across all countries, from the metadata
-- provider: digital (streaming or purchase) and physical (disc). The calendar
-- lists a movie on the earlier of the two, never on its theatrical date,
-- because only a home release can reach a media server. Adding nullable
-- columns without a default only changes the catalog, so it does not rewrite
-- media_items.
ALTER TABLE media_items ADD COLUMN IF NOT EXISTS digital_release_date DATE;
ALTER TABLE media_items ADD COLUMN IF NOT EXISTS physical_release_date DATE;

-- The calendar's movie branch filters on the earlier of the two dates.
-- A failed concurrent build can leave an invalid index; drop it before retry.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_movie_home_release_date;

CREATE INDEX CONCURRENTLY idx_media_items_movie_home_release_date
    ON public.media_items ((LEAST(digital_release_date, physical_release_date)), content_id)
    WHERE type = 'movie';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_movie_home_release_date;
ALTER TABLE media_items DROP COLUMN IF EXISTS physical_release_date;
ALTER TABLE media_items DROP COLUMN IF EXISTS digital_release_date;
