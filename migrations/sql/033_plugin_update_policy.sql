-- +goose Up
-- +goose StatementBegin
ALTER TABLE plugin_installations
    ADD COLUMN update_policy TEXT NOT NULL DEFAULT 'auto',
    ADD COLUMN available_version TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE plugin_installations
    DROP COLUMN update_policy,
    DROP COLUMN available_version;
-- +goose StatementEnd
