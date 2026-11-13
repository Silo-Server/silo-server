-- +goose Up
ALTER TABLE plugin_installations
    ADD COLUMN update_failed_version TEXT,
    ADD COLUMN update_failure_reason TEXT,
    ADD COLUMN update_failed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE plugin_installations
    DROP COLUMN update_failed_at,
    DROP COLUMN update_failure_reason,
    DROP COLUMN update_failed_version;
