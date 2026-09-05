-- +goose Up
-- +goose StatementBegin
-- Drop tables that no code reads or writes, ahead of the 1.0 schema lock.
--
-- 1. plex_sync_* (migration 059) was superseded by webhook_sync_* in migration
--    060, which copied every connection, actor mapping, and item state row into
--    the new tables. Nothing has written to the plex_sync_* tables since. The
--    only remaining Go references were vestigial guards in the orphaned-item
--    cleanup predicate and the content-id merge steps; both are removed in the
--    same change.
-- 2. user_playback_sessions came from the 001 baseline and never had a reader or
--    writer in this repository. Live playback sessions are playback_sessions /
--    playback_sessions_sync.
-- 3. content_id_migration_map is the audit/rollback artifact left behind by
--    migration 20260612130000. Keeping it also keeps that migration's Down able
--    to reverse the content-id remap, so this drop is the point of no return for
--    that rollback; see the Down section below.
--
-- Drop order follows the foreign keys: item_state -> actor_mappings and
-- item_bindings -> connections, so children go first.
DROP TABLE IF EXISTS public.plex_sync_item_state;
DROP TABLE IF EXISTS public.plex_sync_item_bindings;
DROP TABLE IF EXISTS public.plex_sync_actor_mappings;
DROP TABLE IF EXISTS public.plex_sync_connections;

DROP TABLE IF EXISTS public.user_playback_sessions;

DROP TABLE IF EXISTS public.content_id_migration_map;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Reverse by recreating the six tables empty, following the convention set by
-- migration 156: schemas are inlined from their original migrations with the
-- original constraint and index names, so an up-down-up cycle is idempotent.
--
-- Lossy in reverse, deliberately: these tables held no live data, and this
-- migration does not archive their rows anywhere, so the Down cannot restore
-- content. In particular, rolling further back past migration 20260612130000
-- afterwards finds an empty content_id_migration_map: its Down reverts the
-- content-id collation but no longer remaps deterministic ids back to their
-- Sonyflake originals. Restore from a backup instead of relying on that path.

-- BEGIN inlined from migrations/sql/059_plex_sync.sql
CREATE TABLE IF NOT EXISTS public.plex_sync_connections (
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

CREATE TABLE IF NOT EXISTS public.plex_sync_actor_mappings (
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

-- media_item_id is text COLLATE "C" here, not plain text: migration
-- 20260612130000 recollated every content-id column it swept by name, and these
-- two tables were in that sweep.
CREATE TABLE IF NOT EXISTS public.plex_sync_item_bindings (
    connection_id uuid NOT NULL REFERENCES public.plex_sync_connections(id) ON DELETE CASCADE,
    media_item_id text COLLATE pg_catalog."C" NOT NULL,
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

CREATE TABLE IF NOT EXISTS public.plex_sync_item_state (
    mapping_id integer NOT NULL REFERENCES public.plex_sync_actor_mappings(id) ON DELETE CASCADE,
    media_item_id text COLLATE pg_catalog."C" NOT NULL,
    last_plex_state_at timestamptz,
    last_silo_state_at timestamptz,
    last_synced_direction text NOT NULL DEFAULT '',
    last_plex_position_ms bigint NOT NULL DEFAULT 0,
    last_silo_position_ms bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT plex_sync_item_state_pkey PRIMARY KEY (mapping_id, media_item_id)
);

CREATE INDEX IF NOT EXISTS idx_plex_sync_actor_mappings_connection
    ON public.plex_sync_actor_mappings (connection_id);

CREATE INDEX IF NOT EXISTS idx_plex_sync_actor_mappings_profile
    ON public.plex_sync_actor_mappings (silo_profile_id);

CREATE INDEX IF NOT EXISTS idx_plex_sync_item_bindings_connection_last_seen
    ON public.plex_sync_item_bindings (connection_id, last_seen_at DESC);

CREATE INDEX IF NOT EXISTS idx_plex_sync_item_state_mapping_updated
    ON public.plex_sync_item_state (mapping_id, updated_at DESC);
-- END inlined

-- BEGIN inlined from migrations/sql/001_schema.sql
CREATE TABLE IF NOT EXISTS public.user_playback_sessions (
    session_id text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    media_file_id integer NOT NULL,
    play_method text NOT NULL,
    position_seconds double precision DEFAULT 0 NOT NULL,
    is_paused boolean DEFAULT false NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_playback_sessions_pkey PRIMARY KEY (user_id, session_id),
    CONSTRAINT user_playback_sessions_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES public.users(id) ON DELETE CASCADE
);
-- END inlined

-- BEGIN inlined from migrations/sql/20260612130000_deterministic_content_id.sql
CREATE TABLE IF NOT EXISTS public.content_id_migration_map (
    old_id text PRIMARY KEY,
    new_id text NOT NULL,
    entity text NOT NULL,
    status text NOT NULL DEFAULT 'mapped'
);

CREATE INDEX IF NOT EXISTS content_id_migration_map_new_id_idx
    ON public.content_id_migration_map (new_id);
-- END inlined
-- +goose StatementEnd
