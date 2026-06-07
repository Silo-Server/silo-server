-- +goose Up
DELETE FROM server_settings
WHERE key IN ('audiobookshelf_compat.listen', 'audiobookshelf_compat.public_url');

-- +goose Down
SELECT 1;
