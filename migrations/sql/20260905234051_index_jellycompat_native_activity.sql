-- +goose Up
CREATE INDEX idx_jellycompat_playback_sessions_upstream
ON jellycompat_playback_sessions ((data->>'UpstreamSessionID'));

-- +goose Down
DROP INDEX idx_jellycompat_playback_sessions_upstream;
