-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.plex_sync_connections (
    id uuid PRIMARY KEY,
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    plex_server_id text NOT NULL,
    plex_server_name text NOT NULL DEFAULT '',
    plex_base_url text NOT NULL,
    plex_server_token text NOT NULL,
    webhook_secret text NOT NULL,
    bindings_ready boolean NOT NULL DEFAULT false,
    last_webhook_received_at timestamptz,
    last_webhook_error_at timestamptz,
    last_webhook_error_message text,
    last_writeback_at timestamptz,
    last_writeback_error_at timestamptz,
    last_writeback_error_message text,
    last_binding_refresh_at timestamptz,
    last_binding_refresh_error_at timestamptz,
    last_binding_refresh_error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT plex_sync_connections_user_server_unique UNIQUE (user_id, plex_server_id),
    CONSTRAINT plex_sync_connections_webhook_secret_unique UNIQUE (webhook_secret)
);

CREATE TABLE public.plex_sync_actor_mappings (
    id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    connection_id uuid NOT NULL REFERENCES public.plex_sync_connections(id) ON DELETE CASCADE,
    plex_account_id bigint NOT NULL,
    plex_account_title text NOT NULL DEFAULT '',
    silo_profile_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT plex_sync_actor_mappings_connection_account_unique UNIQUE (connection_id, plex_account_id),
    CONSTRAINT plex_sync_actor_mappings_connection_profile_unique UNIQUE (connection_id, silo_profile_id)
);

CREATE TABLE public.plex_sync_item_bindings (
    connection_id uuid NOT NULL REFERENCES public.plex_sync_connections(id) ON DELETE CASCADE,
    media_item_id text NOT NULL,
    plex_rating_key text NOT NULL,
    plex_key text NOT NULL DEFAULT '',
    plex_type text NOT NULL,
    plex_guid text NOT NULL DEFAULT '',
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT plex_sync_item_bindings_pkey PRIMARY KEY (connection_id, media_item_id),
    CONSTRAINT plex_sync_item_bindings_connection_rating_key_unique UNIQUE (connection_id, plex_rating_key)
);

CREATE TABLE public.plex_sync_item_state (
    mapping_id integer NOT NULL REFERENCES public.plex_sync_actor_mappings(id) ON DELETE CASCADE,
    media_item_id text NOT NULL,
    last_plex_state_at timestamptz,
    last_silo_state_at timestamptz,
    last_synced_direction text NOT NULL DEFAULT '',
    last_plex_position_ms bigint NOT NULL DEFAULT 0,
    last_silo_position_ms bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT plex_sync_item_state_pkey PRIMARY KEY (mapping_id, media_item_id)
);

CREATE INDEX idx_plex_sync_actor_mappings_connection
    ON public.plex_sync_actor_mappings (connection_id);

CREATE INDEX idx_plex_sync_actor_mappings_profile
    ON public.plex_sync_actor_mappings (silo_profile_id);

CREATE INDEX idx_plex_sync_item_bindings_connection_last_seen
    ON public.plex_sync_item_bindings (connection_id, last_seen_at DESC);

CREATE INDEX idx_plex_sync_item_state_mapping_updated
    ON public.plex_sync_item_state (mapping_id, updated_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.plex_sync_item_state;
DROP TABLE IF EXISTS public.plex_sync_item_bindings;
DROP TABLE IF EXISTS public.plex_sync_actor_mappings;
DROP TABLE IF EXISTS public.plex_sync_connections;
-- +goose StatementEnd
