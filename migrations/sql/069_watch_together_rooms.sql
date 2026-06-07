-- +goose Up
-- +goose StatementBegin
CREATE TABLE watch_together_rooms (
    id TEXT PRIMARY KEY,
    code TEXT NOT NULL UNIQUE,
    join_token TEXT NOT NULL UNIQUE,
    host_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    host_profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL,
    file_id INTEGER,
    library_id INTEGER,
    guest_control_policy TEXT NOT NULL DEFAULT 'host_only',
    status TEXT NOT NULL DEFAULT 'active',
    anchor_position_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    is_paused BOOLEAN NOT NULL DEFAULT TRUE,
    anchor_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    generation BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ,
    CONSTRAINT watch_together_rooms_guest_control_policy_check
        CHECK (guest_control_policy IN ('host_only', 'guest_play_pause')),
    CONSTRAINT watch_together_rooms_status_check
        CHECK (status IN ('active', 'closed'))
);

CREATE INDEX idx_watch_together_rooms_status_created_at
    ON watch_together_rooms (status, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_watch_together_rooms_status_created_at;
DROP TABLE IF EXISTS watch_together_rooms;
-- +goose StatementEnd
