-- +goose Up
-- The device a login session was opened from, as the client reported it in
-- X-Silo-Device-Id and X-Silo-Device-Platform. Audit and display data only:
-- the values are client-supplied and authorize nothing. NULL for sessions
-- opened before this migration or by clients that send no device headers.
ALTER TABLE auth_sessions
    ADD COLUMN device_id TEXT,
    ADD COLUMN device_platform TEXT;

-- +goose Down
ALTER TABLE auth_sessions
    DROP COLUMN device_platform,
    DROP COLUMN device_id;
