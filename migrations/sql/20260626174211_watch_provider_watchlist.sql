-- +goose Up
-- +goose StatementBegin

-- Profile preference: remove an item from the watchlist once it is fully
-- watched (a movie on completion, a series once every episode is watched).
-- Defaults on; it is a standalone behavior that also propagates to connected
-- watchlist providers.
ALTER TABLE public.user_profiles
    ADD COLUMN remove_watched_from_watchlist boolean NOT NULL DEFAULT true;

-- Per-connection watchlist sync toggles, mirroring the favorites toggles.
ALTER TABLE public.watch_provider_connections
    ADD COLUMN import_watchlist_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN export_watchlist_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN sync_watchlist_removals_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN last_watchlist_sync_at timestamptz;

-- Per-run watchlist counters, mirroring the favorites counters.
ALTER TABLE public.watch_provider_sync_runs
    ADD COLUMN inbound_watchlist_found integer NOT NULL DEFAULT 0,
    ADD COLUMN inbound_watchlist_imported integer NOT NULL DEFAULT 0,
    ADD COLUMN outbound_watchlist_found integer NOT NULL DEFAULT 0,
    ADD COLUMN outbound_watchlist_sent integer NOT NULL DEFAULT 0,
    ADD COLUMN watchlist_removals_sent integer NOT NULL DEFAULT 0;

-- The favorites shadow table generalizes into a list-item shadow table keyed by
-- a list_kind discriminator ('favorites' | 'watchlist'). Existing rows are all
-- favorites, so the new column defaults accordingly.
ALTER TABLE public.watch_provider_favorite_items
    RENAME TO watch_provider_list_items;

ALTER TABLE public.watch_provider_list_items
    ADD COLUMN list_kind text NOT NULL DEFAULT 'favorites';

ALTER TABLE public.watch_provider_list_items
    DROP CONSTRAINT watch_provider_favorite_items_connection_media_key;
ALTER TABLE public.watch_provider_list_items
    ADD CONSTRAINT watch_provider_list_items_connection_kind_media_key
        UNIQUE (connection_id, list_kind, media_item_id);

DROP INDEX IF EXISTS idx_watch_provider_favorite_items_provider_key;
CREATE UNIQUE INDEX idx_watch_provider_list_items_provider_key
    ON public.watch_provider_list_items (connection_id, list_kind, provider_item_key)
    WHERE provider_item_key <> '';

ALTER INDEX idx_watch_provider_favorite_items_connection_remote
    RENAME TO idx_watch_provider_list_items_connection_remote;
ALTER INDEX idx_watch_provider_favorite_items_connection_local
    RENAME TO idx_watch_provider_list_items_connection_local;

-- Re-map MDBList. MDBList exposes a single list — its watchlist — which Silo
-- historically bound to the *favorites* abstraction. Re-bind those connections
-- to the watchlist abstraction so MDBList's watchlist mirrors Silo's watchlist.
-- The stale favorites shadow rows are dropped so the next sync rebuilds clean
-- watchlist state (removals default off, so the first sync is a union, not a
-- purge).
UPDATE public.watch_provider_connections
SET import_watchlist_enabled = import_favorites_enabled,
    export_watchlist_enabled = export_favorites_enabled,
    sync_watchlist_removals_enabled = sync_favorite_removals_enabled,
    last_watchlist_sync_at = last_favorites_sync_at,
    import_favorites_enabled = false,
    export_favorites_enabled = false,
    sync_favorite_removals_enabled = false
WHERE provider = 'mdblist';

DELETE FROM public.watch_provider_list_items
WHERE list_kind = 'favorites'
  AND connection_id IN (
      SELECT id FROM public.watch_provider_connections WHERE provider = 'mdblist'
  );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Re-bind MDBList connections back to the favorites abstraction.
UPDATE public.watch_provider_connections
SET import_favorites_enabled = import_watchlist_enabled,
    export_favorites_enabled = export_watchlist_enabled,
    sync_favorite_removals_enabled = sync_watchlist_removals_enabled,
    last_favorites_sync_at = last_watchlist_sync_at
WHERE provider = 'mdblist';

-- Collapsing list_kind back into a single list requires dropping any
-- watchlist-only rows to avoid violating the restored unique constraint.
DELETE FROM public.watch_provider_list_items
WHERE list_kind <> 'favorites';

ALTER INDEX idx_watch_provider_list_items_connection_local
    RENAME TO idx_watch_provider_favorite_items_connection_local;
ALTER INDEX idx_watch_provider_list_items_connection_remote
    RENAME TO idx_watch_provider_favorite_items_connection_remote;

DROP INDEX IF EXISTS idx_watch_provider_list_items_provider_key;
CREATE UNIQUE INDEX idx_watch_provider_favorite_items_provider_key
    ON public.watch_provider_list_items (connection_id, provider_item_key)
    WHERE provider_item_key <> '';

ALTER TABLE public.watch_provider_list_items
    DROP CONSTRAINT watch_provider_list_items_connection_kind_media_key;
ALTER TABLE public.watch_provider_list_items
    ADD CONSTRAINT watch_provider_favorite_items_connection_media_key
        UNIQUE (connection_id, media_item_id);

ALTER TABLE public.watch_provider_list_items
    DROP COLUMN list_kind;

ALTER TABLE public.watch_provider_list_items
    RENAME TO watch_provider_favorite_items;

ALTER TABLE public.watch_provider_sync_runs
    DROP COLUMN IF EXISTS watchlist_removals_sent,
    DROP COLUMN IF EXISTS outbound_watchlist_sent,
    DROP COLUMN IF EXISTS outbound_watchlist_found,
    DROP COLUMN IF EXISTS inbound_watchlist_imported,
    DROP COLUMN IF EXISTS inbound_watchlist_found;

ALTER TABLE public.watch_provider_connections
    DROP COLUMN IF EXISTS last_watchlist_sync_at,
    DROP COLUMN IF EXISTS sync_watchlist_removals_enabled,
    DROP COLUMN IF EXISTS export_watchlist_enabled,
    DROP COLUMN IF EXISTS import_watchlist_enabled;

ALTER TABLE public.user_profiles
    DROP COLUMN IF EXISTS remove_watched_from_watchlist;
-- +goose StatementEnd
