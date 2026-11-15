-- +goose Up
-- A watchlist entry outside the library has a shadow row named for one TMDB
-- ID. When promotion moves the entry onto the library watchlist, the title and
-- its other IDs are deleted, so the row keeps every ID the title has held
-- ("tmdb:550", "imdb:tt0137523", "tvdb:121361") to find the library item
-- afterwards. Library item rows leave it empty.
ALTER TABLE public.watch_provider_list_items
    ADD COLUMN IF NOT EXISTS title_ids text[] NOT NULL DEFAULT '{}';

-- Rows written before this column take the IDs of the titles their profiles
-- still have entries for, so a promotion before the next sync is still
-- recognized.
UPDATE public.watch_provider_list_items AS item
SET title_ids = ARRAY(
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
    ORDER BY 1)
WHERE item.list_kind = 'watchlist'
  AND item.media_item_id LIKE 'watchlist-title:%';

-- +goose Down
ALTER TABLE public.watch_provider_list_items
    DROP COLUMN IF EXISTS title_ids;
