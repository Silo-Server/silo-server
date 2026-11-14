-- +goose Up
-- Access tokens default to 8h. Every database was seeded with 1h, which
-- overrode it and expired the token baked into a web player's media URL every
-- hour of playback. Move only the old seed; any other value is an
-- administrator's choice and stays. Each refresh extends the login session by
-- the refresh lifetime, and clients refresh once an access token has expired,
-- so the session must outlast an 8h token by a clear margin: otherwise an
-- active client is signed out when the token expires, or after a shorter idle
-- gap than the 1h token allowed. Move it only when the refresh row is absent
-- (30d default) or plainly at least two days in whole days or hours; leave
-- any other refresh value, and the 1h it was paired with, alone.
UPDATE server_settings
SET value = '8h'
WHERE key = 'auth.access_token_expiry'
  AND value = '1h'
  AND NOT EXISTS (
    SELECT 1 FROM server_settings refresh
    WHERE refresh.key = 'auth.refresh_token_expiry'
      AND NOT CASE
        WHEN refresh.value ~ '^[0-9]+d$' THEN left(refresh.value, -1)::numeric >= 2
        WHEN refresh.value ~ '^[0-9]+h$' THEN left(refresh.value, -1)::numeric >= 48
        ELSE false
      END
  );

-- +goose Down
-- Intentionally a no-op: after the update there is no reliable way to
-- distinguish the migrated default from an operator-chosen 8h.
SELECT 1;
