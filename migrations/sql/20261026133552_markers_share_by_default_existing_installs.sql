-- +goose Up
-- A new server shares markers by default only after its admin saves the setup
-- wizard's sharing choice. Servers already set up, which have an account,
-- keep sharing on by default as before. A stored choice stays as it is.
INSERT INTO server_settings (key, value)
VALUES ('markers.share_by_default', 'true')
ON CONFLICT (key) DO NOTHING;

-- A database with no account yet is a new server: leave its choice unset.
DELETE FROM server_settings
WHERE key = 'markers.share_by_default'
  AND value = 'true'
  AND NOT EXISTS (SELECT 1 FROM users);

-- +goose Down
-- Intentionally a no-op: the stored true can't be told apart from an
-- admin's choice.
SELECT 1;
