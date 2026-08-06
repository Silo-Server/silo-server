-- +goose Up
ALTER TABLE watch_provider_connections
    ADD COLUMN plugin_credentials text NOT NULL DEFAULT '';

ALTER TABLE watch_provider_scrobble_sessions
    ADD COLUMN history_reconciled_at timestamptz;

CREATE INDEX watch_provider_scrobble_reconcile_pending_idx
    ON watch_provider_scrobble_sessions (stop_sent_at)
    WHERE stop_sent_at IS NOT NULL
      AND completed = true
      AND history_id <> ''
      AND history_reconciled_at IS NULL;

-- +goose Down
-- Irreversible for plugin connections: TokenType, Scopes, and
-- SecretAttributes exist only in plugin_credentials, so rolling back may
-- require those providers to reconnect.
ALTER TABLE watch_provider_connections
    DROP COLUMN IF EXISTS plugin_credentials;

DROP INDEX IF EXISTS watch_provider_scrobble_reconcile_pending_idx;

ALTER TABLE watch_provider_scrobble_sessions
    DROP COLUMN IF EXISTS history_reconciled_at;
