-- +goose Up
ALTER TABLE watch_provider_connections
    ADD COLUMN plugin_credentials text NOT NULL DEFAULT '';

ALTER TABLE watch_provider_scrobble_sessions
    ADD COLUMN history_reconciled_at timestamptz;

-- +goose Down
-- Irreversible for plugin connections: TokenType, Scopes, and
-- SecretAttributes exist only in plugin_credentials, so rolling back may
-- require those providers to reconnect.
ALTER TABLE watch_provider_connections
    DROP COLUMN IF EXISTS plugin_credentials;

ALTER TABLE watch_provider_scrobble_sessions
    DROP COLUMN IF EXISTS history_reconciled_at;
