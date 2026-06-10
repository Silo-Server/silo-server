-- +goose Up
-- +goose StatementBegin
-- The original ABS compatibility setting copied the stale audiobooks.enabled
-- kill-switch, which defaulted to false. Docker deployments already publish
-- :13378, so keep the listener enabled unless an operator disables it later.
UPDATE server_settings
SET value = 'true'
WHERE key = 'audiobookshelf_compat.enabled'
  AND trim(value) = '';

INSERT INTO server_settings (key, value)
SELECT 'audiobookshelf_compat.enabled', 'true'
WHERE NOT EXISTS (
    SELECT 1
    FROM server_settings
    WHERE key = 'audiobookshelf_compat.enabled'
);

DELETE FROM server_settings
WHERE key = 'audiobooks.enabled';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
INSERT INTO server_settings (key, value)
VALUES ('audiobooks.enabled', 'false')
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd
