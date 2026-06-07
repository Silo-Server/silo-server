-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_media_analyzer_bindings_folder_installation;
DROP TABLE IF EXISTS media_analyzer_bindings;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
-- +goose StatementEnd
