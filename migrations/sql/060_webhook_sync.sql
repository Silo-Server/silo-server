-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.webhook_sync_connections (
    id uuid PRIMARY KEY,
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('plex', 'emby', 'jellyfin')),
    server_id text NOT NULL DEFAULT '',
    server_name text NOT NULL DEFAULT '',
    base_url text NOT NULL DEFAULT '',
    access_token text NOT NULL DEFAULT '',
    default_profile_id text NOT NULL DEFAULT '',
    webhook_secret text NOT NULL,
    account_discovery_available boolean NOT NULL DEFAULT false,
    last_webhook_received_at timestamptz,
    last_webhook_error_at timestamptz,
    last_webhook_error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT webhook_sync_connections_webhook_secret_unique UNIQUE (webhook_secret)
);

CREATE TABLE public.webhook_sync_actor_mappings (
    id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    connection_id uuid NOT NULL REFERENCES public.webhook_sync_connections(id) ON DELETE CASCADE,
    external_actor_id text NOT NULL,
    external_actor_name text NOT NULL DEFAULT '',
    silo_profile_id text,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT webhook_sync_actor_mappings_connection_actor_unique UNIQUE (connection_id, external_actor_id)
);

CREATE TABLE public.webhook_sync_item_state (
    connection_id uuid NOT NULL REFERENCES public.webhook_sync_connections(id) ON DELETE CASCADE,
    external_actor_id text NOT NULL,
    external_item_id text NOT NULL,
    media_item_id text,
    last_event_at timestamptz NOT NULL,
    last_completed boolean NOT NULL DEFAULT false,
    last_position_seconds double precision NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT webhook_sync_item_state_pkey PRIMARY KEY (connection_id, external_actor_id, external_item_id)
);

CREATE INDEX idx_webhook_sync_connections_user
    ON public.webhook_sync_connections (user_id, created_at DESC);

CREATE INDEX idx_webhook_sync_actor_mappings_connection
    ON public.webhook_sync_actor_mappings (connection_id, external_actor_name, id);

CREATE INDEX idx_webhook_sync_actor_mappings_profile
    ON public.webhook_sync_actor_mappings (silo_profile_id);

INSERT INTO public.webhook_sync_connections (
    id,
    user_id,
    provider,
    server_id,
    server_name,
    base_url,
    access_token,
    default_profile_id,
    webhook_secret,
    account_discovery_available,
    last_webhook_received_at,
    last_webhook_error_at,
    last_webhook_error_message,
    created_at,
    updated_at
)
SELECT
    c.id,
    c.user_id,
    'plex',
    c.plex_server_id,
    c.plex_server_name,
    c.plex_base_url,
    c.plex_server_token,
    '',
    c.webhook_secret,
    false,
    c.last_webhook_received_at,
    c.last_webhook_error_at,
    c.last_webhook_error_message,
    c.created_at,
    c.updated_at
FROM public.plex_sync_connections c
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.webhook_sync_actor_mappings (
    connection_id,
    external_actor_id,
    external_actor_name,
    silo_profile_id,
    last_seen_at,
    created_at,
    updated_at
)
SELECT
    m.connection_id,
    m.plex_account_id::text,
    m.plex_account_title,
    m.silo_profile_id,
    COALESCE(c.last_webhook_received_at, m.updated_at, m.created_at, now()),
    m.created_at,
    m.updated_at
FROM public.plex_sync_actor_mappings m
JOIN public.plex_sync_connections c ON c.id = m.connection_id
ON CONFLICT (connection_id, external_actor_id) DO NOTHING;

INSERT INTO public.webhook_sync_item_state (
    connection_id,
    external_actor_id,
    external_item_id,
    media_item_id,
    last_event_at,
    last_completed,
    last_position_seconds,
    updated_at
)
SELECT
    m.connection_id,
    m.plex_account_id::text,
    COALESCE(b.plex_rating_key, s.media_item_id),
    s.media_item_id,
    COALESCE(s.last_plex_state_at, s.updated_at, now()),
    s.last_plex_position_ms > 0 AND s.last_plex_position_ms >= s.last_silo_position_ms,
    GREATEST(s.last_plex_position_ms, 0)::double precision / 1000.0,
    s.updated_at
FROM public.plex_sync_item_state s
JOIN public.plex_sync_actor_mappings m ON m.id = s.mapping_id
LEFT JOIN public.plex_sync_item_bindings b
    ON b.connection_id = m.connection_id
   AND b.media_item_id = s.media_item_id
ON CONFLICT (connection_id, external_actor_id, external_item_id) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.webhook_sync_item_state;
DROP TABLE IF EXISTS public.webhook_sync_actor_mappings;
DROP TABLE IF EXISTS public.webhook_sync_connections;
-- +goose StatementEnd
