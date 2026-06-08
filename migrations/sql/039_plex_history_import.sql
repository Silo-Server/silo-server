-- +goose Up
-- +goose StatementBegin
-- Expand source_type to allow 'plex'
ALTER TABLE history_import_sources
    DROP CONSTRAINT history_import_sources_source_type_check,
    ADD CONSTRAINT history_import_sources_source_type_check
        CHECK (source_type IN ('emby', 'plex'));

ALTER TABLE history_import_runs
    DROP CONSTRAINT history_import_runs_source_type_check,
    ADD CONSTRAINT history_import_runs_source_type_check
        CHECK (source_type IN ('emby', 'plex'));

ALTER TABLE history_import_runs
    DROP CONSTRAINT history_import_runs_connection_mode_check,
    ADD CONSTRAINT history_import_runs_connection_mode_check
        CHECK (connection_mode IN ('connect', 'predefined', 'custom', 'plex_oauth'));

-- Plex OAuth sessions (parallel to history_import_connect_sessions)
CREATE TABLE history_import_plex_sessions (
    id text PRIMARY KEY,
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pin_id text NOT NULL,
    pin_code text NOT NULL,
    auth_token text,
    servers_json jsonb NOT NULL DEFAULT '[]'::jsonb,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_history_import_plex_sessions_user_expires
    ON history_import_plex_sessions (user_id, expires_at DESC);

CREATE INDEX idx_history_import_plex_sessions_expiry
    ON history_import_plex_sessions (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS history_import_plex_sessions;

ALTER TABLE history_import_runs
    DROP CONSTRAINT history_import_runs_connection_mode_check,
    ADD CONSTRAINT history_import_runs_connection_mode_check
        CHECK (connection_mode IN ('connect', 'predefined', 'custom'));

ALTER TABLE history_import_runs
    DROP CONSTRAINT history_import_runs_source_type_check,
    ADD CONSTRAINT history_import_runs_source_type_check
        CHECK (source_type IN ('emby'));

ALTER TABLE history_import_sources
    DROP CONSTRAINT history_import_sources_source_type_check,
    ADD CONSTRAINT history_import_sources_source_type_check
        CHECK (source_type IN ('emby'));
-- +goose StatementEnd
