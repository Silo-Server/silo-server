-- +goose Up
CREATE TABLE plugin_provision_status (
    plugin_id text PRIMARY KEY,
    version text NOT NULL DEFAULT '',
    state text NOT NULL,
    detail text NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE plugin_provision_status;
