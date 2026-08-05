-- +goose Up
ALTER TABLE watch_provider_connections
    ADD COLUMN plugin_credentials text NOT NULL DEFAULT '';

ALTER TABLE watch_provider_scrobble_sessions
    ADD COLUMN history_reconciled_at timestamptz;

-- +goose Down
ALTER TABLE watch_provider_connections
    DROP COLUMN IF EXISTS plugin_credentials;

ALTER TABLE watch_provider_scrobble_sessions
    DROP COLUMN IF EXISTS history_reconciled_at;
