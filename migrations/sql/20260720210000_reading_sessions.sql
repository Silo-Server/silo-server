-- +goose Up
CREATE TABLE IF NOT EXISTS reading_sessions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    last_heartbeat_at TIMESTAMPTZ NOT NULL,
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    start_fraction REAL NOT NULL,
    end_fraction REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reading_sessions_recency
    ON reading_sessions (user_id, profile_id, last_heartbeat_at);
CREATE INDEX IF NOT EXISTS idx_reading_sessions_book
    ON reading_sessions (user_id, profile_id, content_id);

-- +goose Down
DROP TABLE IF EXISTS reading_sessions;
