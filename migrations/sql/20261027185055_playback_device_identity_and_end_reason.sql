-- Facts a monitoring tool needs to tell plays apart after they end. device_id
-- and device_platform are the device the client declared in its
-- X-Silo-Device-* headers (the profile device registry's identity); a
-- Jellyfin play carries the DeviceId from its MediaBrowser authorization and
-- no platform. client_name, client_version and the per-stream decisions are
-- the session's when it ended. end_reason is why the attempt ended, and
-- end_reason_at the end instant of the finalization that recorded it: a
-- finalization that gets no further leaves ended_at alone, so the reason
-- needs its own ordering. Every column is NULL on rows written before it
-- existed and when the server did not know the value.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE admin_playback_history
    ADD COLUMN IF NOT EXISTS device_id TEXT,
    ADD COLUMN IF NOT EXISTS device_platform TEXT,
    ADD COLUMN IF NOT EXISTS client_name TEXT,
    ADD COLUMN IF NOT EXISTS client_version TEXT,
    ADD COLUMN IF NOT EXISTS video_decision TEXT,
    ADD COLUMN IF NOT EXISTS audio_decision TEXT,
    ADD COLUMN IF NOT EXISTS end_reason TEXT,
    ADD COLUMN IF NOT EXISTS end_reason_at TIMESTAMPTZ;

ALTER TABLE playback_sessions_sync
    ADD COLUMN IF NOT EXISTS device_id TEXT,
    ADD COLUMN IF NOT EXISTS device_platform TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE playback_sessions_sync
    DROP COLUMN IF EXISTS device_platform,
    DROP COLUMN IF EXISTS device_id;

ALTER TABLE admin_playback_history
    DROP COLUMN IF EXISTS end_reason_at,
    DROP COLUMN IF EXISTS end_reason,
    DROP COLUMN IF EXISTS audio_decision,
    DROP COLUMN IF EXISTS video_decision,
    DROP COLUMN IF EXISTS client_version,
    DROP COLUMN IF EXISTS client_name,
    DROP COLUMN IF EXISTS device_platform,
    DROP COLUMN IF EXISTS device_id;
-- +goose StatementEnd
