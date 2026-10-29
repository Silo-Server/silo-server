-- +goose NO TRANSACTION

-- +goose Up
-- The earliest digital (streaming or purchase) release of a movie across all
-- countries, from the metadata provider. The calendar lists a movie on the
-- earlier of release_date and this date. Adding a nullable column without a
-- default only changes the catalog, so it does not rewrite media_items.
ALTER TABLE media_items ADD COLUMN IF NOT EXISTS digital_release_date DATE;

-- The calendar's movie branch filters on the earlier of the two dates.
-- A failed concurrent build can leave an invalid index; drop it before retry.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_movie_calendar_date;

CREATE INDEX CONCURRENTLY idx_media_items_movie_calendar_date
    ON public.media_items ((LEAST(release_date, digital_release_date)), content_id)
    WHERE type = 'movie';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_movie_calendar_date;
ALTER TABLE media_items DROP COLUMN IF EXISTS digital_release_date;
