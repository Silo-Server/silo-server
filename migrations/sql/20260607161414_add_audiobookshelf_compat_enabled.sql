-- +goose Up
-- +goose StatementBegin
INSERT INTO server_settings (key, value)
VALUES ('audiobookshelf_compat.enabled', 'true')
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM server_settings WHERE key = 'audiobookshelf_compat.enabled';
-- +goose StatementEnd
