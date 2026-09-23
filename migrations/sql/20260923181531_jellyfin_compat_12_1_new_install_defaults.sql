-- +goose Up
-- Jellyfin compatibility defaults move to the 12.1 line for new installs only.
-- Configured servers keep what they report and install until an admin
-- changes it in the Jellyfin compatibility settings.

-- Fresh databases inherit the 10.12.0 seed (a version Jellyfin never released)
-- before setup; start them on Jellyfin 12.1.
UPDATE server_settings
SET value = '12.1.0'
WHERE key = 'jellyfin_compat.emulated_server_version'
  AND value = '10.12.0'
  AND NOT EXISTS (SELECT 1 FROM users)
  AND NOT EXISTS (
      SELECT 1 FROM server_settings
      WHERE key = 'setup.completed' AND lower(trim(value)) = 'true'
  );

-- The managed Jellyfin Web version is not seeded: a missing or empty row
-- follows the built-in default, which is now 12.1. Pin configured servers
-- that never stored one to the previous default, 10.11.6, so their Web
-- install target does not change under them.
UPDATE server_settings
SET value = '10.11.6'
WHERE key = 'jellyfin_compat.web_version'
  AND value ~ '^[[:space:]]*$'
  AND (
      EXISTS (SELECT 1 FROM users)
      OR EXISTS (
          SELECT 1 FROM server_settings
          WHERE key = 'setup.completed' AND lower(trim(value)) = 'true'
      )
  );

INSERT INTO server_settings (key, value)
SELECT 'jellyfin_compat.web_version', '10.11.6'
WHERE NOT EXISTS (
      SELECT 1 FROM server_settings WHERE key = 'jellyfin_compat.web_version'
  )
  AND (
      EXISTS (SELECT 1 FROM users)
      OR EXISTS (
          SELECT 1 FROM server_settings
          WHERE key = 'setup.completed' AND lower(trim(value)) = 'true'
      )
  );

-- +goose Down
-- Settings may have been customized after setup; a rollback must preserve them.
SELECT 1;
