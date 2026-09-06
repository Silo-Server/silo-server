-- +goose NO TRANSACTION

-- +goose Up
-- Session writes continue during the build. A failed concurrent build can
-- leave an invalid index; remove it before retrying without blocking writers.
DROP INDEX CONCURRENTLY IF EXISTS idx_jellycompat_playback_sessions_upstream;
CREATE INDEX CONCURRENTLY idx_jellycompat_playback_sessions_upstream
ON jellycompat_playback_sessions ((data->>'UpstreamSessionID'));

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_jellycompat_playback_sessions_upstream;
