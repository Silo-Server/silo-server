-- +goose Up
CREATE TABLE plugin_provision_placement (
    plugin_installation_id bigint PRIMARY KEY REFERENCES plugin_installations(id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE plugin_provision_placement;
