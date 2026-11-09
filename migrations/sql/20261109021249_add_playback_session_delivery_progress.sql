-- +goose Up
-- How far a stream is delivered ahead of the viewer, and how far its transcode
-- has produced, in media seconds. NULL means the serving node did not report it.
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN IF NOT EXISTS delivered_through_seconds DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS produced_through_seconds DOUBLE PRECISION;

-- +goose Down
ALTER TABLE public.playback_sessions_sync
    DROP COLUMN IF EXISTS delivered_through_seconds,
    DROP COLUMN IF EXISTS produced_through_seconds;
