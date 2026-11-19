-- +goose Up
-- Watchlist title rows written before title_ids existed take the IDs of the
-- titles their profiles still have entries for, so a promotion before the
-- next sync is still recognized. It runs apart from the column add, so it
-- holds no table lock beyond the rows it changes. A row that already holds
-- every derived ID is left alone, and the derived IDs are added to the ones a
-- row kept, so running it again changes nothing.
WITH derived AS (
    SELECT item.id, ARRAY(
        SELECT DISTINCT alias.provider || ':' || alias.provider_id
        FROM public.watch_provider_connections AS conn
        JOIN public.user_watchlist_titles AS entry
          ON entry.user_id = conn.user_id AND entry.profile_id = conn.profile_id
        JOIN public.watchlist_title_aliases AS listed
          ON listed.title_id = entry.title_id
         AND listed.provider = 'tmdb'
         AND listed.media_type = split_part(item.media_item_id, ':', 2)
         AND listed.provider_id = split_part(item.media_item_id, ':', 3)
        JOIN public.watchlist_title_aliases AS alias ON alias.title_id = entry.title_id
        WHERE conn.id = item.connection_id
    ) AS ids
    FROM public.watch_provider_list_items AS item
    WHERE item.list_kind = 'watchlist'
      AND item.media_item_id LIKE 'watchlist-title:%'
)
UPDATE public.watch_provider_list_items AS item
SET title_ids = ARRAY(SELECT DISTINCT id FROM unnest(item.title_ids || derived.ids) AS ids(id) ORDER BY id),
    updated_at = now()
FROM derived
WHERE item.id = derived.id
  AND NOT item.title_ids @> derived.ids;

-- +goose Down
-- The IDs are a superset of what sync would write; the column's own migration
-- drops them.
SELECT 1;
