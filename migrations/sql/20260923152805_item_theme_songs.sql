-- +goose Up
CREATE TABLE item_theme_songs (
    id BIGSERIAL PRIMARY KEY,
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    owner_path TEXT NOT NULL,
    file_path TEXT NOT NULL,
    title TEXT NOT NULL,
    duration_seconds INTEGER NOT NULL CHECK (duration_seconds >= 0),
    container TEXT NOT NULL,
    audio_codec TEXT NOT NULL,
    audio_channels INTEGER NOT NULL,
    bitrate_kbps INTEGER NOT NULL,
    sample_rate INTEGER NOT NULL,
    file_size BIGINT NOT NULL CHECK (file_size > 0),
    file_modified_at TIMESTAMPTZ NOT NULL,
    UNIQUE (media_folder_id, file_path)
);
CREATE INDEX item_theme_songs_owner ON item_theme_songs(media_folder_id, owner_path);

-- +goose Down
DROP TABLE item_theme_songs;
