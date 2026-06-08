-- +goose Up
-- +goose StatementBegin
-- Allow 'jellyfin' as a source_type for history import sources.
-- Previously only 'emby' and 'plex' were allowed because jellyfin self-service
-- imports used inline credentials rather than predefined sources.
ALTER TABLE history_import_sources
    DROP CONSTRAINT history_import_sources_source_type_check,
    ADD CONSTRAINT history_import_sources_source_type_check
        CHECK (source_type IN ('emby', 'jellyfin', 'plex'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE history_import_sources
    DROP CONSTRAINT history_import_sources_source_type_check,
    ADD CONSTRAINT history_import_sources_source_type_check
        CHECK (source_type IN ('emby', 'plex'));
-- +goose StatementEnd
