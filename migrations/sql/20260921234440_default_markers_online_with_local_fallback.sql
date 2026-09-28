-- +goose Up
-- Fresh databases inherit the previous online-only seed before setup. Keep
-- every configured server's mode and all other explicit marker choices.
UPDATE server_settings
SET value = 'both'
WHERE key = 'markers.mode'
  AND value = 'online'
  AND NOT EXISTS (SELECT 1 FROM users)
  AND NOT EXISTS (
      SELECT 1 FROM server_settings
      WHERE key = 'setup.completed' AND lower(trim(value)) = 'true'
  );

-- +goose Down
-- Settings may have been customized after setup; a rollback must preserve them.
SELECT 1;
