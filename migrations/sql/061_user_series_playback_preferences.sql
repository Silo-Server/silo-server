-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS user_series_playback_preferences (
    user_id INTEGER NOT NULL,
    profile_id TEXT NOT NULL,
    series_id TEXT NOT NULL,
    resolution TEXT,
    hdr BOOLEAN NOT NULL DEFAULT false,
    codec_video TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, series_id),
    CONSTRAINT user_series_playback_preferences_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS user_series_playback_preferences;
-- +goose StatementEnd
