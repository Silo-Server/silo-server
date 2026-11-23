-- +goose Up
ALTER TABLE plugin_auth_bindings
    ADD COLUMN IF NOT EXISTS callback_mode TEXT NOT NULL DEFAULT 'installation'
    CHECK (callback_mode IN ('installation', 'stable'));

-- +goose Down
ALTER TABLE plugin_auth_bindings DROP COLUMN callback_mode;
