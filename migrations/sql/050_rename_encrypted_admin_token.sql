-- +goose Up
-- +goose StatementBegin
-- Rename column: encryption was removed; plain "admin_token" is clearer.
ALTER TABLE history_import_sources RENAME COLUMN encrypted_admin_token TO admin_token;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE history_import_sources RENAME COLUMN admin_token TO encrypted_admin_token;
-- +goose StatementEnd
