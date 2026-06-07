-- +goose Up
-- +goose StatementBegin
ALTER TABLE user_audio_preferences
    ADD COLUMN IF NOT EXISTS audio_track_signature JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE user_subtitle_preferences
    ADD COLUMN IF NOT EXISTS subtitle_track_signature JSONB NOT NULL DEFAULT '{}'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE user_audio_preferences
    DROP COLUMN IF EXISTS audio_track_signature;

ALTER TABLE user_subtitle_preferences
    DROP COLUMN IF EXISTS subtitle_track_signature;
-- +goose StatementEnd
