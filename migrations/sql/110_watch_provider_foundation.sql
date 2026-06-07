-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.watch_provider_connections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    provider_account_id text NOT NULL DEFAULT '',
    provider_username text NOT NULL DEFAULT '',
    access_token text NOT NULL DEFAULT '',
    refresh_token text NOT NULL DEFAULT '',
    token_expires_at timestamptz,
    import_watched_enabled boolean NOT NULL DEFAULT true,
    import_progress_enabled boolean NOT NULL DEFAULT true,
    export_watched_enabled boolean NOT NULL DEFAULT true,
    scrobble_enabled boolean NOT NULL DEFAULT true,
    last_inbound_sync_at timestamptz,
    last_progress_sync_at timestamptz,
    last_outbound_sync_at timestamptz,
    last_scrobble_error_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT watch_provider_connections_profile_fkey
        FOREIGN KEY (user_id, profile_id)
        REFERENCES public.user_profiles(user_id, id)
        ON DELETE CASCADE,
    CONSTRAINT watch_provider_connections_provider_profile_key
        UNIQUE (provider, user_id, profile_id)
);

CREATE INDEX idx_watch_provider_connections_provider_enabled
    ON public.watch_provider_connections (provider, user_id, profile_id);

CREATE TABLE public.watch_provider_auth_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    device_code text NOT NULL,
    user_code text NOT NULL,
    verification_url text NOT NULL,
    interval_seconds integer NOT NULL DEFAULT 5,
    expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT watch_provider_auth_sessions_profile_fkey
        FOREIGN KEY (user_id, profile_id)
        REFERENCES public.user_profiles(user_id, id)
        ON DELETE CASCADE
);

CREATE INDEX idx_watch_provider_auth_sessions_lookup
    ON public.watch_provider_auth_sessions (provider, user_id, profile_id, expires_at DESC);

CREATE TABLE public.watch_provider_sync_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL REFERENCES public.watch_provider_connections(id) ON DELETE CASCADE,
    trigger text NOT NULL,
    status text NOT NULL,
    provider text NOT NULL,
    inbound_watched_found integer NOT NULL DEFAULT 0,
    inbound_watched_imported integer NOT NULL DEFAULT 0,
    inbound_progress_found integer NOT NULL DEFAULT 0,
    inbound_progress_imported integer NOT NULL DEFAULT 0,
    outbound_found integer NOT NULL DEFAULT 0,
    outbound_sent integer NOT NULL DEFAULT 0,
    warning text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_watch_provider_sync_runs_connection_started
    ON public.watch_provider_sync_runs (connection_id, started_at DESC);

CREATE TABLE public.watch_provider_history_exports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL REFERENCES public.watch_provider_connections(id) ON DELETE CASCADE,
    history_id text NOT NULL,
    media_item_id text NOT NULL,
    watched_at timestamptz NOT NULL,
    provider_item_key text NOT NULL,
    status text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT watch_provider_history_exports_connection_history_key
        UNIQUE (connection_id, history_id)
);

CREATE INDEX idx_watch_provider_history_exports_status
    ON public.watch_provider_history_exports (connection_id, status, watched_at ASC);

CREATE TABLE public.watch_provider_scrobble_sessions (
    playback_session_id text NOT NULL,
    connection_id uuid NOT NULL REFERENCES public.watch_provider_connections(id) ON DELETE CASCADE,
    media_item_id text NOT NULL,
    provider_item_key text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    imdb_id text NOT NULL DEFAULT '',
    tmdb_id text NOT NULL DEFAULT '',
    tvdb_id text NOT NULL DEFAULT '',
    series_imdb_id text NOT NULL DEFAULT '',
    series_tmdb_id text NOT NULL DEFAULT '',
    series_tvdb_id text NOT NULL DEFAULT '',
    season_number integer NOT NULL DEFAULT 0,
    episode_number integer NOT NULL DEFAULT 0,
    history_id text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    last_progress double precision NOT NULL DEFAULT 0,
    duration_seconds double precision NOT NULL DEFAULT 0,
    completed boolean NOT NULL DEFAULT false,
    last_action text NOT NULL DEFAULT '',
    stop_sent_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT watch_provider_scrobble_sessions_pkey
        PRIMARY KEY (playback_session_id, connection_id)
);

CREATE INDEX idx_watch_provider_scrobble_sessions_open
    ON public.watch_provider_scrobble_sessions (connection_id, started_at ASC)
    WHERE stop_sent_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.watch_provider_scrobble_sessions;
DROP TABLE IF EXISTS public.watch_provider_history_exports;
DROP TABLE IF EXISTS public.watch_provider_sync_runs;
DROP TABLE IF EXISTS public.watch_provider_auth_sessions;
DROP TABLE IF EXISTS public.watch_provider_connections;
-- +goose StatementEnd
