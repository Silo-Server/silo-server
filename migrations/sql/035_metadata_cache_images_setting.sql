-- +goose Up
-- +goose StatementBegin
INSERT INTO server_settings (key, value)
VALUES ('metadata.cache_images', 'false')
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd
