-- +goose Up
-- +goose StatementBegin

-- Protocol v3 is now the server's only playback protocol, so both of its
-- rollout flags have lost their meaning. `playback.protocol_v3_enabled` would
-- read as "no playback at all" when false, and
-- `playback.protocol_v3_shadow_enabled` gated a shadow planner that compared v3
-- against the legacy start path — a path that no longer exists. Neither row was
-- ever exposed in a settings UI, so nothing reads them.
DELETE FROM server_settings
WHERE key IN ('playback.protocol_v3_enabled', 'playback.protocol_v3_shadow_enabled');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Restore the rows at the values a rolled-back server would expect: v3 enabled
-- (the state 20260714180000 established) and shadow comparison off.
INSERT INTO server_settings (key, value)
VALUES ('playback.protocol_v3_enabled', 'true')
ON CONFLICT (key) DO UPDATE SET value = 'true';

INSERT INTO server_settings (key, value)
VALUES ('playback.protocol_v3_shadow_enabled', 'false')
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd
