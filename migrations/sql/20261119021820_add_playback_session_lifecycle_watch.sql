-- +goose Up
-- watch is the session's watch-time measurement (playback.WatchTime as JSON)
-- as the server holding the session last saved it: when the session starts,
-- then on each live-session sync. A session ended on a server that never
-- held it, or rebuilt after a restart, carries on from it instead of
-- starting with no measurement. NULL until a server saves one. Writing it
-- does not touch updated_at, which the idle sweep reads as activity.
ALTER TABLE playback_session_lifecycle ADD COLUMN IF NOT EXISTS watch JSONB;

-- +goose Down
ALTER TABLE playback_session_lifecycle DROP COLUMN IF EXISTS watch;
