-- +goose Up
-- +goose StatementBegin
-- Denormalized "last aired episode date" on media_items. Maintained by
-- the episode upsert path. Replaces the per-row correlated subquery in
-- catalog/air_date_sql.go (audit 2026-05-01 §2.1, hot path #1).

ALTER TABLE public.media_items
ADD COLUMN IF NOT EXISTS last_air_date_at date;

-- Backfill from existing episodes.
UPDATE public.media_items mi
SET last_air_date_at = sub.last_aired
FROM (
    SELECT e.series_id, MAX(e.air_date) AS last_aired
    FROM public.episodes e
    WHERE e.air_date IS NOT NULL AND e.air_date <= CURRENT_DATE
    GROUP BY e.series_id
) sub
WHERE mi.content_id = sub.series_id
  AND mi.type = 'series';

CREATE INDEX IF NOT EXISTS idx_media_items_last_air_date_at
ON public.media_items USING btree (last_air_date_at DESC NULLS LAST)
WHERE type = 'series';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_items_last_air_date_at;
ALTER TABLE public.media_items DROP COLUMN IF EXISTS last_air_date_at;
-- +goose StatementEnd
