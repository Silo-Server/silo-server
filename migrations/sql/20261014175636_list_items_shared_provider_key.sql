-- +goose NO TRANSACTION

-- +goose Up
-- Two library items can share a provider key: a movie and a series with the
-- same TMDB id and no IMDb id, or two items for one title that carry the same
-- IMDb id. Each is its own favorites or watchlist entry in Silo, so each keeps
-- its own shadow row; only (connection_id, list_kind, media_item_id) stays
-- unique, as on the ratings and dropped-show shadow tables. A unique key index
-- failed every list sync that held two such items.
--
-- The indexes are built and dropped concurrently so list syncs keep reading
-- and writing the table. The migration only runs when it is not yet recorded,
-- so a new index found here is a leftover of an interrupted build.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_provider_list_items_key;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_watch_provider_list_items_key
    ON public.watch_provider_list_items (connection_id, list_kind, provider_item_key)
    WHERE provider_item_key <> '';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_provider_list_items_provider_key;

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

-- Up removed the unique index, so one found here is a leftover of an
-- interrupted rollback, possibly invalid.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_provider_list_items_provider_key;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_watch_provider_list_items_provider_key
    ON public.watch_provider_list_items (connection_id, list_kind, provider_item_key)
    WHERE provider_item_key <> '';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_provider_list_items_key;
