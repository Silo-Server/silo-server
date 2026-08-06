-- +goose Up
INSERT INTO server_settings (key, value)
VALUES ('virtual_playback_prewarm_enabled', 'false')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM server_settings WHERE key = 'virtual_playback_prewarm_enabled';
