-- +goose Up
-- +goose StatementBegin
INSERT INTO server_settings (key, value)
SELECT 'server.public_url', value
FROM server_settings
WHERE key = 'notifications.email.external_url'
  AND value <> ''
  AND NOT EXISTS (
    SELECT 1 FROM server_settings
    WHERE key = 'server.public_url' AND value <> ''
  )
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
WHERE server_settings.value = '';

DELETE FROM server_settings WHERE key = 'notifications.email.external_url';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The old setting is intentionally not restored; its value was migrated into
-- server.public_url and cannot be safely split back from the canonical key.
-- +goose StatementEnd
