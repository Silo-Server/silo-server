-- Master kill-switch for the absorbed audiobooks feature. Defaults to
-- 'false' so landing migrations 139-142 is a no-op for users; operators
-- flip this to 'true' at cutover.

INSERT INTO server_settings (key, value) VALUES ('audiobooks.enabled', 'false')
ON CONFLICT (key) DO NOTHING;
