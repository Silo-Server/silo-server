-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.playback_sessions_sync
    ADD COLUMN IF NOT EXISTS client_ip inet,
    ADD COLUMN IF NOT EXISTS audio_track_index integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS transcode_audio boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS stream_bitrate_kbps integer,
    ADD COLUMN IF NOT EXISTS transcode_node_url text,
    ADD COLUMN IF NOT EXISTS target_resolution text,
    ADD COLUMN IF NOT EXISTS target_video_codec text,
    ADD COLUMN IF NOT EXISTS target_audio_codec text,
    ADD COLUMN IF NOT EXISTS target_bitrate_kbps integer;
-- +goose StatementEnd
