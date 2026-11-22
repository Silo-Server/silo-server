-- +goose Up
CREATE TABLE IF NOT EXISTS plugin_provision_ownership (
    plugin_installation_id BIGINT PRIMARY KEY REFERENCES plugin_installations(id) ON DELETE CASCADE,
    version_pin TEXT NOT NULL DEFAULT '',
    config_fields JSONB NOT NULL DEFAULT '{}',
    binding_fields JSONB NOT NULL DEFAULT '{}'
);

-- +goose Down
DROP TABLE plugin_provision_ownership;
