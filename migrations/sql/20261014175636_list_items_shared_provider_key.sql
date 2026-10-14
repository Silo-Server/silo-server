-- +goose Up
-- Two library items can share a provider key: a movie and a series with the
-- same TMDB id and no IMDb id, or two items for one title that carry the same
-- IMDb id. Each is its own favorites or watchlist entry in Silo, so each keeps
-- its own shadow row; only (connection_id, list_kind, media_item_id) stays
-- unique, as on the ratings and dropped-show shadow tables. A unique key index
-- failed every list sync that held two such items.
DROP INDEX IF EXISTS idx_watch_provider_list_items_provider_key;
CREATE INDEX idx_watch_provider_list_items_provider_key
    ON public.watch_provider_list_items (connection_id, list_kind, provider_item_key)
    WHERE provider_item_key <> '';

-- +goose Down
-- The unique index allows one row per key, so keep each key's most recently
-- updated row.
DELETE FROM public.watch_provider_list_items AS stale
USING public.watch_provider_list_items AS kept
WHERE stale.provider_item_key <> ''
  AND stale.connection_id = kept.connection_id
  AND stale.list_kind = kept.list_kind
  AND stale.provider_item_key = kept.provider_item_key
  AND (stale.updated_at, stale.id) < (kept.updated_at, kept.id);

DROP INDEX IF EXISTS idx_watch_provider_list_items_provider_key;
CREATE UNIQUE INDEX idx_watch_provider_list_items_provider_key
    ON public.watch_provider_list_items (connection_id, list_kind, provider_item_key)
    WHERE provider_item_key <> '';
