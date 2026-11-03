-- watched_seconds records where a play ended, not how much of it was
-- watched: a play resumed at the one-hour mark records an hour after a minute
-- of viewing. These columns hold the session's measured watch time (see
-- playback.WatchTime). measured is false for rows written before this
-- migration and for plays that never reported progress; readers fall back to
-- the old wall-clock-capped estimate for those rows. measured_since and
-- measured_until bound the samples behind the measurement, so a session
-- rebuilt and finalized again can be told apart from another copy of the
-- same stretch.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE admin_playback_history
    ADD COLUMN IF NOT EXISTS start_position_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS played_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS paused_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS measured BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS measured_since TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS measured_until TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE admin_playback_history
    DROP COLUMN IF EXISTS measured_until,
    DROP COLUMN IF EXISTS measured_since,
    DROP COLUMN IF EXISTS measured,
    DROP COLUMN IF EXISTS paused_seconds,
    DROP COLUMN IF EXISTS played_seconds,
    DROP COLUMN IF EXISTS start_position_seconds;
-- +goose StatementEnd
