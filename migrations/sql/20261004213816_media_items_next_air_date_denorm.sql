-- +goose NO TRANSACTION

-- +goose Up
-- Denormalized "next episode air date" on media_items, next to the
-- last_air_date_at denorm (migration 103). last_air_date_at depends on the
-- current date, so it goes stale when a known future episode airs without any
-- episode write. next_air_date_at records when that happens: the
-- refresh_series_air_dates task recomputes series whose next_air_date_at has
-- passed. Both columns are maintained by refreshSeriesAirDatesSQL in
-- internal/catalog/episode_repo.go.
--
-- NO TRANSACTION keeps ACCESS EXCLUSIVE on media_items to the column add,
-- which is metadata-only for a nullable column without a default. The
-- backfill then commits on its own and holds row locks only on series whose
-- values change, and the index is built CONCURRENTLY. Every statement is safe
-- to rerun after a partial failure.

ALTER TABLE public.media_items
ADD COLUMN IF NOT EXISTS next_air_date_at date;

-- Backfill both columns. This also repairs last_air_date_at values that went
-- stale before the sweep existed. The aggregate reads
-- idx_episodes_series_air_date.
UPDATE public.media_items mi
SET last_air_date_at = sub.last_aired,
    next_air_date_at = sub.next_airing
FROM (
    SELECT e.series_id,
           MAX(e.air_date) FILTER (WHERE e.air_date <= CURRENT_DATE) AS last_aired,
           MIN(e.air_date) FILTER (WHERE e.air_date > CURRENT_DATE) AS next_airing
    FROM public.episodes e
    WHERE e.air_date IS NOT NULL
    GROUP BY e.series_id
) sub
WHERE mi.content_id = sub.series_id
  AND mi.type = 'series'
  AND (mi.last_air_date_at IS DISTINCT FROM sub.last_aired
       OR mi.next_air_date_at IS DISTINCT FROM sub.next_airing);

-- Series without any dated episode get no row above. Clear a leftover
-- last_air_date_at there (for example after an air-date edit removed the
-- last date, which did not recompute before this migration); with
-- next_air_date_at NULL the sweep would never revisit it.
UPDATE public.media_items mi
SET last_air_date_at = NULL
WHERE mi.type = 'series'
  AND mi.last_air_date_at IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM public.episodes e
      WHERE e.series_id = mi.content_id AND e.air_date IS NOT NULL
  );

-- A failed nontransactional run can leave an invalid index behind; drop it
-- concurrently so a retry rebuilds it without a write-blocking DROP INDEX.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_next_air_date_at;
CREATE INDEX CONCURRENTLY idx_media_items_next_air_date_at
ON public.media_items USING btree (next_air_date_at)
WHERE type = 'series' AND next_air_date_at IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_next_air_date_at;
ALTER TABLE public.media_items DROP COLUMN IF EXISTS next_air_date_at;
