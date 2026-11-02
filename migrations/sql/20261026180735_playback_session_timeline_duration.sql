-- +goose Up
-- The length of the timeline a session's position is measured on, when that
-- is more than its file: a whole Audiobookshelf book of several files. NULL
-- means the file's own duration.
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN timeline_duration_seconds double precision;

-- +goose Down
ALTER TABLE public.playback_sessions_sync
    DROP COLUMN timeline_duration_seconds;
