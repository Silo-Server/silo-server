-- +goose NO TRANSACTION

-- +goose Up
-- Denormalized "newest aired episode with a file" on media_items, the
-- "Latest Episode Air Date" sort and rule key (issue #2741). Unlike
-- last_air_date_at (migration 103), which names the show's newest aired
-- episode for the series' last_air_date field, it skips episodes the
-- metadata provider lists but no library has a file for. Membership is
-- episode_libraries, as for latest_episode_added_at. refreshSeriesAirDatesSQL
-- in internal/catalog/episode_repo.go keeps it current.
--
-- NO TRANSACTION keeps ACCESS EXCLUSIVE on media_items to the column add,
-- which is metadata-only for a nullable column without a default. The
-- backfill then commits on its own and holds row locks only on series whose
-- value changes, and the index is built CONCURRENTLY. Every statement is safe
-- to rerun after a partial failure.

ALTER TABLE public.media_items
ADD COLUMN IF NOT EXISTS latest_available_episode_air_date_at date;

-- Backfill from existing episodes and their library memberships. Every
-- series row is a candidate, so a rerun also clears a value whose last
-- qualifying episode went away since the first run.
UPDATE public.media_items mi
SET latest_available_episode_air_date_at = sub.last_aired
FROM (
    SELECT s.content_id AS series_id, MAX(e.air_date) AS last_aired
    FROM public.media_items s
    LEFT JOIN public.episodes e
      ON e.series_id = s.content_id
     AND e.air_date IS NOT NULL
     AND e.air_date <= CURRENT_DATE
     AND EXISTS (
         SELECT 1 FROM public.episode_libraries el
         WHERE el.episode_id = e.content_id
     )
    WHERE s.type = 'series'
    GROUP BY s.content_id
) sub
WHERE mi.content_id = sub.series_id
  AND mi.latest_available_episode_air_date_at IS DISTINCT FROM sub.last_aired;

-- A failed nontransactional run can leave an invalid index behind; drop it
-- concurrently so a retry rebuilds it without a write-blocking DROP INDEX.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_latest_available_episode_air_date_at;
CREATE INDEX CONCURRENTLY idx_media_items_latest_available_episode_air_date_at
ON public.media_items USING btree (latest_available_episode_air_date_at DESC NULLS LAST)
WHERE type = 'series';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_latest_available_episode_air_date_at;
ALTER TABLE public.media_items DROP COLUMN IF EXISTS latest_available_episode_air_date_at;
