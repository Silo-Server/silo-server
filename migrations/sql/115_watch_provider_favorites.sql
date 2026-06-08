-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.watch_provider_connections
    ADD COLUMN import_favorites_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN export_favorites_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN sync_favorite_removals_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN last_favorites_sync_at timestamptz;

ALTER TABLE public.watch_provider_sync_runs
    ADD COLUMN inbound_favorites_found integer NOT NULL DEFAULT 0,
    ADD COLUMN inbound_favorites_imported integer NOT NULL DEFAULT 0,
    ADD COLUMN outbound_favorites_found integer NOT NULL DEFAULT 0,
    ADD COLUMN outbound_favorites_sent integer NOT NULL DEFAULT 0,
    ADD COLUMN favorite_removals_sent integer NOT NULL DEFAULT 0;

CREATE TABLE public.watch_provider_favorite_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL REFERENCES public.watch_provider_connections(id) ON DELETE CASCADE,
    media_item_id text NOT NULL,
    provider_item_key text NOT NULL,
    kind text NOT NULL,
    title text NOT NULL DEFAULT '',
    year integer NOT NULL DEFAULT 0,
    remote_present boolean NOT NULL DEFAULT false,
    local_present boolean NOT NULL DEFAULT false,
    last_seen_remote_at timestamptz,
    last_seen_local_at timestamptz,
    last_exported_at timestamptz,
    last_removed_remote_at timestamptz,
    last_removed_local_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT watch_provider_favorite_items_connection_media_key
        UNIQUE (connection_id, media_item_id)
);

CREATE UNIQUE INDEX idx_watch_provider_favorite_items_provider_key
    ON public.watch_provider_favorite_items (connection_id, provider_item_key)
    WHERE provider_item_key <> '';

CREATE INDEX idx_watch_provider_favorite_items_connection_remote
    ON public.watch_provider_favorite_items (connection_id, remote_present);

CREATE INDEX idx_watch_provider_favorite_items_connection_local
    ON public.watch_provider_favorite_items (connection_id, local_present);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.watch_provider_favorite_items;

ALTER TABLE public.watch_provider_sync_runs
    DROP COLUMN IF EXISTS favorite_removals_sent,
    DROP COLUMN IF EXISTS outbound_favorites_sent,
    DROP COLUMN IF EXISTS outbound_favorites_found,
    DROP COLUMN IF EXISTS inbound_favorites_imported,
    DROP COLUMN IF EXISTS inbound_favorites_found;

ALTER TABLE public.watch_provider_connections
    DROP COLUMN IF EXISTS last_favorites_sync_at,
    DROP COLUMN IF EXISTS sync_favorite_removals_enabled,
    DROP COLUMN IF EXISTS export_favorites_enabled,
    DROP COLUMN IF EXISTS import_favorites_enabled;
-- +goose StatementEnd
