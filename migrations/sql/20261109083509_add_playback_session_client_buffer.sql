-- +goose Up
-- The media time the client reported buffered through with its latest progress
-- sample. NULL means it did not report one.
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN IF NOT EXISTS client_buffered_through_seconds DOUBLE PRECISION;

-- +goose Down
ALTER TABLE public.playback_sessions_sync
    DROP COLUMN IF EXISTS client_buffered_through_seconds;
