-- +goose Up
-- +goose StatementBegin
-- Expand run source_type to allow 'jellyfin'
ALTER TABLE history_import_runs
    DROP CONSTRAINT history_import_runs_source_type_check,
    ADD CONSTRAINT history_import_runs_source_type_check
        CHECK (source_type IN ('emby', 'jellyfin', 'plex'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE history_import_runs
    DROP CONSTRAINT history_import_runs_source_type_check,
    ADD CONSTRAINT history_import_runs_source_type_check
        CHECK (source_type IN ('emby', 'plex'));
-- +goose StatementEnd
