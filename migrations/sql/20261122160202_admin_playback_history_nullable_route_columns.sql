-- +goose Up
-- Ensure delivered route-fact text columns on admin_playback_history are
-- nullable without empty-string defaults. Environments that previously ran
-- earlier development schemas could already hold these columns as
-- TEXT NOT NULL DEFAULT '', which ADD COLUMN IF NOT EXISTS preserved and
-- caused direct-play RecordHistory inserts (passing NULL for unused transcode
-- targets) to fail with SQLSTATE 23502.
-- +goose StatementBegin
ALTER TABLE admin_playback_history
    ALTER COLUMN target_video_codec DROP NOT NULL,
    ALTER COLUMN target_video_codec DROP DEFAULT,
    ALTER COLUMN target_resolution DROP NOT NULL,
    ALTER COLUMN target_resolution DROP DEFAULT,
    ALTER COLUMN target_audio_codec DROP NOT NULL,
    ALTER COLUMN target_audio_codec DROP DEFAULT,
    ALTER COLUMN stream_location DROP NOT NULL,
    ALTER COLUMN stream_location DROP DEFAULT;

UPDATE admin_playback_history
SET
    target_video_codec = NULLIF(target_video_codec, ''),
    target_resolution  = NULLIF(target_resolution, ''),
    target_audio_codec = NULLIF(target_audio_codec, ''),
    stream_location    = NULLIF(stream_location, '')
WHERE target_video_codec = ''
   OR target_resolution = ''
   OR target_audio_codec = ''
   OR stream_location = '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
