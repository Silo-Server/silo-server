-- +goose Up
INSERT INTO server_settings (key, value)
VALUES ('playback.virtual_provider_failover', 'true')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM server_settings WHERE key = 'playback.virtual_provider_failover';
