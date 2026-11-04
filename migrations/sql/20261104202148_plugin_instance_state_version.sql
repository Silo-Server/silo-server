-- +goose Up
-- +goose StatementBegin
-- Write counter per plugin_instance_state row: 1 after the first write, one
-- more after each later write. A plugin passes the version it read as
-- WriteInstanceState.expected_version so that two API servers changing one
-- key cannot overwrite each other. Existing rows start at 1. The version is
-- not part of the state_value AAD, so existing ciphertexts stay readable.
ALTER TABLE plugin_instance_state
    ADD COLUMN version BIGINT NOT NULL DEFAULT 1 CHECK (version >= 1);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE plugin_instance_state DROP COLUMN IF EXISTS version;
-- +goose StatementEnd
