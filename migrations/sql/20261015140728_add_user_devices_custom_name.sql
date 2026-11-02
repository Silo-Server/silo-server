-- +goose Up
-- A name the profile chose for one of its devices, shown in place of the name
-- the client reports (several Apple TVs all report "Apple TV"). Registration
-- never writes it, so a device keeps its name as it keeps being seen. NULL: no
-- name chosen; device_name is shown. The per-user SQLite store adds the same
-- column in its schema v32.
ALTER TABLE user_devices ADD COLUMN IF NOT EXISTS custom_name text;

-- +goose Down
ALTER TABLE user_devices DROP COLUMN IF EXISTS custom_name;
