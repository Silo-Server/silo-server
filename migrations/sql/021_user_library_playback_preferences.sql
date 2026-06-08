-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS user_library_playback_preferences (
    user_id INTEGER NOT NULL,
    profile_id TEXT NOT NULL,
    library_id INTEGER NOT NULL,
    audio_language TEXT,
    subtitle_language TEXT,
    subtitle_mode TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, library_id),
    CONSTRAINT user_library_playback_preferences_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
-- +goose StatementEnd
