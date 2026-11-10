-- playback_session_lifecycle holds each playback session's announced state
-- (playing, paused, stopped) and the sequence number of its last transition,
-- so the admin sessions channel can tell monitors when sessions start, pause,
-- resume and stop. Every transition takes the next value of one global
-- sequence in the statement that makes it, so a session's numbers rise in the
-- order the database accepted its transitions, across API servers.
--
-- The start columns are written once and are enough to list a session no
-- server holds in memory and to record its end when nothing richer is left.
-- A stopped row is kept for a day as a tombstone, so a late start or pause
-- for the same session announces nothing.
--
-- admin_playback_history.lifecycle_seq is the sequence number of the end that
-- the row records; it orders a history row against live observations.

-- +goose Up
-- +goose StatementBegin
CREATE SEQUENCE IF NOT EXISTS playback_session_lifecycle_seq;

CREATE TABLE IF NOT EXISTS playback_session_lifecycle (
    -- Every playback session id is a UUID; the type lets the attempt join
    -- (playback_v3_attempts.session_id) use its index.
    session_id UUID PRIMARY KEY,
    frontend TEXT NOT NULL CHECK (frontend IN ('native', 'bridge', 'jellyfin', 'abs')),
    state TEXT NOT NULL CHECK (state IN ('playing', 'paused', 'stopped')),
    end_reason TEXT,
    seq BIGINT NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    media_file_id INTEGER NOT NULL,
    requested_media_file_id INTEGER NOT NULL,
    progress_persistence_disabled BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ NOT NULL,
    start_facts JSONB NOT NULL DEFAULT '{}'::jsonb,
    activity_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((state = 'stopped') = (end_reason IS NOT NULL)),
    CHECK (jsonb_typeof(start_facts) = 'object')
);

-- The live sweep and the tombstone retention each scan one side.
CREATE INDEX IF NOT EXISTS idx_playback_session_lifecycle_live
    ON playback_session_lifecycle (updated_at) WHERE state <> 'stopped';
CREATE INDEX IF NOT EXISTS idx_playback_session_lifecycle_stopped
    ON playback_session_lifecycle (updated_at) WHERE state = 'stopped';

ALTER TABLE admin_playback_history
    ADD COLUMN IF NOT EXISTS lifecycle_seq BIGINT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE admin_playback_history DROP COLUMN IF EXISTS lifecycle_seq;
DROP TABLE IF EXISTS playback_session_lifecycle;
DROP SEQUENCE IF EXISTS playback_session_lifecycle_seq;
-- +goose StatementEnd
