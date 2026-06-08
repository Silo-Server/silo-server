-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS movie_match_queue (
    media_file_id INTEGER PRIMARY KEY REFERENCES media_files(id) ON DELETE CASCADE,
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    first_queued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempted_at TIMESTAMPTZ NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_movie_match_queue_available
    ON movie_match_queue (available_at, last_attempted_at, media_folder_id, media_file_id);

CREATE INDEX IF NOT EXISTS idx_movie_match_queue_folder
    ON movie_match_queue (media_folder_id, media_file_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS movie_match_queue;
-- +goose StatementEnd
