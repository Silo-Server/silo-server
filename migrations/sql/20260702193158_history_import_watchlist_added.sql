-- +goose Up
-- +goose StatementBegin
-- Plex watchlist import (issue #245): count of watchlist entries added to
-- the importing profile during a history import run.
ALTER TABLE history_import_runs
ADD COLUMN watchlist_added integer NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE history_import_runs DROP COLUMN IF EXISTS watchlist_added;
-- +goose StatementEnd
