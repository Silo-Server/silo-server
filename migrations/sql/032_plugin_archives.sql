-- +goose Up
-- +goose StatementBegin
CREATE TABLE plugin_archives (
    plugin_installation_id BIGINT PRIMARY KEY REFERENCES plugin_installations(id) ON DELETE CASCADE,
    manifest_json          BYTEA NOT NULL,
    checksum               TEXT NOT NULL,
    archive_bytes          BYTEA NOT NULL,
    CONSTRAINT plugin_archives_manifest_json_not_empty CHECK (octet_length(manifest_json) > 0),
    CONSTRAINT plugin_archives_checksum_not_empty CHECK (length(checksum) > 0),
    CONSTRAINT plugin_archives_archive_bytes_not_empty CHECK (octet_length(archive_bytes) > 0),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS plugin_archives;
-- +goose StatementEnd
