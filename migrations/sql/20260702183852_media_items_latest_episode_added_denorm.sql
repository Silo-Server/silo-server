-- +goose Up
-- +goose StatementBegin
-- Denormalized "latest episode file added" on media_items, powering the
-- "Latest Episodes" sort (issue #202): series ordered by when their newest
-- episode FILE arrived, not by when the series itself was first added.
-- Source of truth is episode_libraries.first_seen_at (per-episode first-seen,
-- stamped from media_files.created_at at link time). Maintained by the three
-- episode_libraries insert paths (FileRepository.UpdateEpisodeLink,
-- FileRepository.BulkLinkEpisodesBySeries, scanner folder-restore), which
-- bump the parent series in the same statement. Mirrors the
-- last_air_date_at denorm (migration 103).
--
-- Like last_air_date_at, the value is global (not access-scoped): a viewer
-- without access to the folder that received the newest episode still sees
-- the series sorted by that arrival. Accepted trade-off for an O(1) sort key.

ALTER TABLE public.media_items
ADD COLUMN IF NOT EXISTS latest_episode_added_at timestamptz;

-- Backfill from existing episode links.
UPDATE public.media_items mi
SET latest_episode_added_at = sub.latest_added
FROM (
    SELECT e.series_id, MAX(el.first_seen_at) AS latest_added
    FROM public.episode_libraries el
    JOIN public.episodes e ON e.content_id = el.episode_id
    GROUP BY e.series_id
) sub
WHERE mi.content_id = sub.series_id
  AND mi.type = 'series';

CREATE INDEX IF NOT EXISTS idx_media_items_latest_episode_added_at
ON public.media_items USING btree (latest_episode_added_at DESC NULLS LAST)
WHERE type = 'series';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_items_latest_episode_added_at;
ALTER TABLE public.media_items DROP COLUMN IF EXISTS latest_episode_added_at;
-- +goose StatementEnd
