-- +goose Up
-- +goose StatementBegin
-- Count successfully applied Emby favorites during history import runs.
ALTER TABLE history_import_runs
ADD COLUMN favorites_imported integer NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE history_import_runs DROP COLUMN IF EXISTS favorites_imported;
-- +goose StatementEnd
