-- +goose Up
-- +goose StatementBegin
-- The original ABS compatibility setting copied the stale audiobooks.enabled
-- kill-switch, which defaulted to false. Docker deployments already publish
-- :13378, so keep the listener enabled unless an operator disables it later.
UPDATE server_settings
SET value = 'true'
WHERE key = 'audiobookshelf_compat.enabled'
  AND lower(trim(value)) IN ('', '0', 'false', 'no', 'off');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE server_settings
SET value = 'false'
WHERE key = 'audiobookshelf_compat.enabled'
  AND lower(trim(value)) = 'true';
-- +goose StatementEnd
