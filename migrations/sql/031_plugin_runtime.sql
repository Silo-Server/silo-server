-- +goose Up
-- +goose StatementBegin
CREATE TABLE plugin_repositories (
    id              BIGSERIAL PRIMARY KEY,
    url             TEXT NOT NULL UNIQUE,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    display_name    TEXT NOT NULL,
    last_fetched_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_plugin_repositories_enabled
    ON plugin_repositories (enabled);

CREATE TABLE plugin_installations (
    id            BIGSERIAL PRIMARY KEY,
    repository_id BIGINT REFERENCES plugin_repositories(id) ON DELETE SET NULL,
    plugin_id     TEXT NOT NULL,
    version       TEXT NOT NULL,
    install_path  TEXT NOT NULL,
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_plugin_installations_plugin_version
    ON plugin_installations (plugin_id, version);

CREATE TABLE plugin_capabilities (
    id                     BIGSERIAL PRIMARY KEY,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    capability_type        TEXT NOT NULL,
    capability_id          TEXT NOT NULL,
    metadata               JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_plugin_capabilities_installation
    ON plugin_capabilities (plugin_installation_id);

CREATE UNIQUE INDEX idx_plugin_capabilities_installation_capability
    ON plugin_capabilities (plugin_installation_id, capability_type, capability_id);

CREATE TABLE plugin_runtime_configs (
    id                     BIGSERIAL PRIMARY KEY,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    config_key             TEXT NOT NULL,
    config_value           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_plugin_runtime_configs_installation_key
    ON plugin_runtime_configs (plugin_installation_id, config_key);

CREATE TABLE plugin_task_bindings (
    id                     BIGSERIAL PRIMARY KEY,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    capability_id          TEXT NOT NULL,
    enabled                BOOLEAN NOT NULL DEFAULT true,
    trigger                JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_plugin_task_bindings_installation_capability
    ON plugin_task_bindings (plugin_installation_id, capability_id);

CREATE TABLE plugin_auth_bindings (
    id                     BIGSERIAL PRIMARY KEY,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    capability_id          TEXT NOT NULL,
    enabled                BOOLEAN NOT NULL DEFAULT true,
    display_order          INTEGER NOT NULL DEFAULT 0,
    auto_provision         BOOLEAN NOT NULL DEFAULT false,
    default_login          BOOLEAN NOT NULL DEFAULT false,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_plugin_auth_bindings_installation_capability
    ON plugin_auth_bindings (plugin_installation_id, capability_id);

CREATE TABLE plugin_auth_identities (
    id                     BIGSERIAL PRIMARY KEY,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    external_subject       TEXT NOT NULL,
    user_id                INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_plugin_auth_identities_installation_subject
    ON plugin_auth_identities (plugin_installation_id, external_subject);

CREATE TABLE media_analyzer_bindings (
    id                     BIGSERIAL PRIMARY KEY,
    media_folder_id        INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    plugin_installation_id BIGINT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    enabled                BOOLEAN NOT NULL DEFAULT true,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_media_analyzer_bindings_folder_installation
    ON media_analyzer_bindings (media_folder_id, plugin_installation_id);

ALTER TABLE media_files
    ADD COLUMN markers_source TEXT,
    ADD COLUMN markers_confidence DOUBLE PRECISION;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_files
    DROP COLUMN markers_confidence,
    DROP COLUMN markers_source;

DROP INDEX IF EXISTS idx_media_analyzer_bindings_folder_installation;
DROP TABLE IF EXISTS media_analyzer_bindings;

DROP INDEX IF EXISTS idx_plugin_auth_identities_installation_subject;
DROP TABLE IF EXISTS plugin_auth_identities;

DROP INDEX IF EXISTS idx_plugin_auth_bindings_installation_capability;
DROP TABLE IF EXISTS plugin_auth_bindings;

DROP INDEX IF EXISTS idx_plugin_task_bindings_installation_capability;
DROP TABLE IF EXISTS plugin_task_bindings;

DROP INDEX IF EXISTS idx_plugin_runtime_configs_installation_key;
DROP TABLE IF EXISTS plugin_runtime_configs;

DROP INDEX IF EXISTS idx_plugin_capabilities_installation_capability;
DROP INDEX IF EXISTS idx_plugin_capabilities_installation;
DROP TABLE IF EXISTS plugin_capabilities;

DROP INDEX IF EXISTS idx_plugin_installations_plugin_version;
DROP TABLE IF EXISTS plugin_installations;

DROP INDEX IF EXISTS idx_plugin_repositories_enabled;
DROP TABLE IF EXISTS plugin_repositories;
-- +goose StatementEnd
