-- +goose Up
-- +goose StatementBegin

-- Per-connection rating sync toggles. Existing connections start with rating
-- sync off: turning it on writes to the user's external account, so it must be
-- an explicit choice. New connections are created with both on by the service.
ALTER TABLE public.watch_provider_connections
    ADD COLUMN import_ratings_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN export_ratings_enabled boolean NOT NULL DEFAULT false;

-- Per-run rating counters. Removals count as sent.
ALTER TABLE public.watch_provider_sync_runs
    ADD COLUMN inbound_ratings_found integer NOT NULL DEFAULT 0,
    ADD COLUMN inbound_ratings_imported integer NOT NULL DEFAULT 0,
    ADD COLUMN outbound_ratings_found integer NOT NULL DEFAULT 0,
    ADD COLUMN outbound_ratings_sent integer NOT NULL DEFAULT 0;

-- The last rating Silo and the provider agreed on, per connection and item, in
-- Silo stars. It is the base of the three-way merge that decides which side
-- changed. No row means the two sides agreed the item is unrated.
-- remote_seen records that a provider read confirmed the agreed rating, so an
-- item missing from a later complete snapshot counts as a remote removal only
-- when the provider is known to have held it. provider_account_id scopes the
-- row to the account it was agreed with; rows of another account are ignored.
CREATE TABLE public.watch_provider_rating_items (
    connection_id uuid NOT NULL
        REFERENCES public.watch_provider_connections(id) ON DELETE CASCADE,
    provider_account_id text NOT NULL DEFAULT '',
    media_item_id text NOT NULL,
    kind text NOT NULL,
    provider_item_key text NOT NULL DEFAULT '',
    synced_rating smallint NOT NULL CHECK (synced_rating BETWEEN 1 AND 5),
    remote_seen boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, media_item_id)
);

CREATE INDEX idx_watch_provider_rating_items_media
    ON public.watch_provider_rating_items (media_item_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.watch_provider_rating_items;

ALTER TABLE public.watch_provider_sync_runs
    DROP COLUMN IF EXISTS outbound_ratings_sent,
    DROP COLUMN IF EXISTS outbound_ratings_found,
    DROP COLUMN IF EXISTS inbound_ratings_imported,
    DROP COLUMN IF EXISTS inbound_ratings_found;

ALTER TABLE public.watch_provider_connections
    DROP COLUMN IF EXISTS export_ratings_enabled,
    DROP COLUMN IF EXISTS import_ratings_enabled;
-- +goose StatementEnd
