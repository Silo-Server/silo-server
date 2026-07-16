-- +goose Up
-- +goose StatementBegin

-- Playback protocol v3 now defaults to enabled. The flag shipped seeded to
-- 'false' as a rollout safety valve, but it was never exposed in any settings
-- UI, so a stored 'false' means "never touched" rather than an explicit
-- opt-out — and the current Android clients are v3-only for video: against a
-- server with the flag off they refuse playback with a misleading "update
-- your server" error. Flip it everywhere.
INSERT INTO server_settings (key, value)
VALUES ('playback.protocol_v3_enabled', 'true')
ON CONFLICT (key) DO UPDATE SET value = 'true';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE server_settings
SET value = 'false'
WHERE key = 'playback.protocol_v3_enabled';
-- +goose StatementEnd
