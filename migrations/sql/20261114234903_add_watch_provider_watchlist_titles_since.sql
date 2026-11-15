-- +goose Up
-- When a connection's first watchlist import that keeps titles the library
-- doesn't have finished. Sync requests only the titles it adds after that
-- import, so connecting a provider, or the first sync after upgrading, doesn't
-- request the whole backlog of its watchlist.
ALTER TABLE public.watch_provider_connections
    ADD COLUMN IF NOT EXISTS watchlist_titles_since timestamptz;

-- +goose Down
ALTER TABLE public.watch_provider_connections
    DROP COLUMN IF EXISTS watchlist_titles_since;
