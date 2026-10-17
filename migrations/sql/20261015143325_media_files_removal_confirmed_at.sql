-- +goose Up
-- When Sonarr/Radarr reported deleting this file itself (an upgrade or a
-- manual delete) and Silo saw it gone from disk. Once a scan marks the file
-- missing, it skips scanner.file_removal_grace on the trash sweep. Cleared
-- whenever a scan finds the file on disk. NULL: no such report.
ALTER TABLE media_files ADD COLUMN IF NOT EXISTS removal_confirmed_at timestamptz;

-- +goose Down
ALTER TABLE media_files DROP COLUMN IF EXISTS removal_confirmed_at;
