-- +goose Up
-- The admin activity views label a transcode's delivered audio, and without the
-- target channel count they had to borrow the source's — rendering a TrueHD 7.1
-- source downmixed to AAC 5.1 as "AAC 7.1". The planner already resolves the
-- encoded channel count, so carry it alongside the target codec. NULL means the
-- reporting node did not know it; consumers must show no channel layout rather
-- than falling back to the source count.
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN IF NOT EXISTS target_audio_channels integer;

-- +goose Down
ALTER TABLE public.playback_sessions_sync
    DROP COLUMN IF EXISTS target_audio_channels;
