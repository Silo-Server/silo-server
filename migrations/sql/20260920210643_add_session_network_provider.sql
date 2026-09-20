-- +goose Up
ALTER TABLE playback_sessions_sync ADD COLUMN routing_network_provider TEXT;
COMMENT ON COLUMN playback_sessions_sync.routing_network_provider IS
    'Validated access provider selected for playback; empty means default network, NULL means unknown.';

-- +goose Down
ALTER TABLE playback_sessions_sync DROP COLUMN routing_network_provider;
