-- +goose Up
-- A Jellyfin-compatible client cannot be asked to replan: after a direct play
-- fails, some clients post a device profile that refuses direct play, and the
-- rest simply retry PlaybackInfo with the profile that still claims the file.
-- This records the failure server-side so the retry is negotiated onto a
-- packaged route (HLS remux or transcode) instead of the bytes that just
-- failed. Rows are scoped to the authenticated compat token's hash, the client
-- device, and the media file, and expire after the device-profile TTL; the
-- write path prunes expired rows for its token, so the table stays small.
CREATE TABLE jellycompat_direct_play_failures (
    token_hash TEXT        NOT NULL,
    device_id  TEXT        NOT NULL,
    file_id    BIGINT      NOT NULL,
    failed_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (token_hash, device_id, file_id)
);

CREATE INDEX jellycompat_direct_play_failures_expiry_idx
    ON jellycompat_direct_play_failures (failed_at);

-- +goose Down
DROP TABLE jellycompat_direct_play_failures;
