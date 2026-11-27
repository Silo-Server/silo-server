-- +goose Up
-- last_air_date_at no longer counts specials (season 0): a special that aired
-- after the finale moved a series' end date in lists past the provider's
-- last_air_date the item endpoint reports (issue #5393). A series with no
-- aired regular episode keeps its newest aired special. Recompute it for
-- series with an aired special, the same way refreshSeriesAirDatesSQL in
-- internal/catalog/episode_repo.go now does. Only series whose value changes
-- are written. next_air_date_at and latest_available_episode_air_date_at
-- still count specials and are left alone.
UPDATE public.media_items mi
SET last_air_date_at = sub.last_aired
FROM (
    SELECT e.series_id,
           COALESCE(
               MAX(e.air_date) FILTER (
                   WHERE e.air_date <= CURRENT_DATE AND e.season_number <> 0
               ),
               MAX(e.air_date) FILTER (WHERE e.air_date <= CURRENT_DATE)
           ) AS last_aired
    FROM public.episodes e
    WHERE e.air_date IS NOT NULL
      AND e.series_id IN (
          SELECT s.series_id FROM public.episodes s
          WHERE s.season_number = 0
            AND s.air_date <= CURRENT_DATE
      )
    GROUP BY e.series_id
) sub
WHERE mi.content_id = sub.series_id
  AND mi.type = 'series'
  AND mi.last_air_date_at IS DISTINCT FROM sub.last_aired;

-- +goose Down
-- Intentionally a no-op, like the repo's other data-only backfills. Silo has
-- no supported in-place downgrade (docs/release-versioning.md): rolling back
-- means restoring the pre-upgrade backup, which holds the old values. Under an
-- older binary a skipped value only leaves a special out of the series' list
-- end date, and its next episode write recomputes it the old way.
SELECT 1;
