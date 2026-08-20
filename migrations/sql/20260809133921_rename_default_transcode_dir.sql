-- +goose Up
UPDATE server_settings
SET value = '/tmp/silo-transcode'
WHERE key = 'playback.transcode_dir'
  AND value = '/tmp/streamapp-transcode';

-- +goose Down
-- Intentionally a no-op: after the update there is no reliable way to
-- distinguish the migrated default from an operator-chosen path.
SELECT 1;
