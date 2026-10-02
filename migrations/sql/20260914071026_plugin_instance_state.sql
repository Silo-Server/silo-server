-- +goose Up
-- +goose StatementBegin
-- Per-instance private state for resident plugins (network access providers
-- keep their overlay node keys here). One row per installation, host scope and
-- key. host_scope is derived by the host ('api' or 'node:<stream_nodes.id>'),
-- never supplied by the plugin. state_value is an encrypted GCM envelope
-- bound to the row: AAD = RowAAD("plugin_instance_state", "state_value",
-- "<installation_id>:<host_scope>:<state_key>"). The API never returns it.
CREATE TABLE plugin_instance_state (
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    host_scope  TEXT   NOT NULL,
    state_key   TEXT   NOT NULL,
    state_value BYTEA  NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (plugin_installation_id, host_scope, state_key)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS plugin_instance_state;
-- +goose StatementEnd
