-- +goose Up
-- The lookback cutoff an admin history import run was queued with. A run with
-- a cutoff reads only Jellyfin items last played at or after it; NULL reads the
-- full history, as every run queued before this column existed did. It is set
-- once at admission and never updated.
ALTER TABLE history_import_runs ADD COLUMN played_since TIMESTAMPTZ;

-- +goose Down
ALTER TABLE history_import_runs DROP COLUMN played_since;
