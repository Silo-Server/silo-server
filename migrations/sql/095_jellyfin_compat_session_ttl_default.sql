-- +goose Up
-- +goose StatementBegin
UPDATE server_settings
SET value = '87600h'
WHERE key = 'jellyfin_compat.session_ttl'
  AND value = '24h';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE server_settings
SET value = '24h'
WHERE key = 'jellyfin_compat.session_ttl'
  AND value = '87600h';
-- +goose StatementEnd
