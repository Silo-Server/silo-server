-- +goose Up
ALTER TABLE plugin_installations
    ADD COLUMN runtime_generation BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE plugin_installations DROP COLUMN runtime_generation;
