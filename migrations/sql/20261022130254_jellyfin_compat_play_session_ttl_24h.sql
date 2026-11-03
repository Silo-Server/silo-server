-- +goose Up
-- Jellyfin play sessions default to 24h, the lifetime the compat router was
-- written for. Every database was seeded with 6h, which overrode it and ended
-- paused or long playback after six hours. Move only the old seed; any other
-- value is an administrator's choice and stays.
UPDATE server_settings
SET value = '24h'
WHERE key = 'jellyfin_compat.playback_session_ttl'
  AND value = '6h';

-- +goose Down
-- Intentionally a no-op: after the update there is no reliable way to
-- distinguish the migrated default from an operator-chosen 24h.
SELECT 1;
