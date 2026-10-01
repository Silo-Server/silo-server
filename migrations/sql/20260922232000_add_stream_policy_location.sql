-- +goose Up
-- Empty means the session predates policy-location capture.
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN stream_location text NOT NULL DEFAULT ''
    CONSTRAINT playback_sessions_sync_stream_location_check
        CHECK (stream_location IN ('', 'local', 'remote'));

-- +goose Down
ALTER TABLE public.playback_sessions_sync
    DROP COLUMN stream_location;
