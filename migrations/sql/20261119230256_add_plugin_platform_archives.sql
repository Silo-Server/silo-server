-- +goose Up
-- Builds of an installed plugin for other platforms. plugin_archives holds
-- the package the installing server downloaded, which is a binary for that
-- server's platform only. A server on another platform (an arm64 node next
-- to amd64 ones) fetches the same version's build for its own platform from
-- the catalog and stores it here, so it and other servers on that platform
-- don't fetch it again.
--
-- base_checksum is the plugin_archives checksum the row was fetched for. A
-- row whose base_checksum no longer matches is from an earlier package and
-- is ignored, so nothing that replaces plugin_archives has to clear it.
-- signature_state and signature_source_repo are the build's own release
-- signature check; a build is used only while it proves as much as the
-- installed build's does.
-- +goose StatementBegin
CREATE TABLE plugin_platform_archives (
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    os                     TEXT NOT NULL,
    arch                   TEXT NOT NULL,
    base_checksum          TEXT NOT NULL,
    manifest_json          BYTEA NOT NULL,
    checksum               TEXT NOT NULL,
    archive_bytes          BYTEA NOT NULL,
    signature_state        TEXT NOT NULL,
    signature_source_repo  TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (plugin_installation_id, os, arch),
    CONSTRAINT plugin_platform_archives_platform_not_empty CHECK (length(os) > 0 AND length(arch) > 0),
    CONSTRAINT plugin_platform_archives_base_checksum_not_empty CHECK (length(base_checksum) > 0),
    CONSTRAINT plugin_platform_archives_manifest_json_not_empty CHECK (octet_length(manifest_json) > 0),
    CONSTRAINT plugin_platform_archives_checksum_not_empty CHECK (length(checksum) > 0),
    CONSTRAINT plugin_platform_archives_archive_bytes_not_empty CHECK (octet_length(archive_bytes) > 0)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS plugin_platform_archives;
-- +goose StatementEnd
