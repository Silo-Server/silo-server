-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS series_root_match_queue (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    observed_root_path TEXT NOT NULL,
    first_queued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempted_at TIMESTAMPTZ NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_folder_id, observed_root_path)
);

CREATE INDEX IF NOT EXISTS idx_series_root_match_queue_attempt
    ON series_root_match_queue (last_attempted_at ASC NULLS FIRST, media_folder_id, observed_root_path);

CREATE INDEX IF NOT EXISTS idx_series_root_match_queue_folder
    ON series_root_match_queue (media_folder_id, observed_root_path);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS series_root_match_queue;
-- +goose StatementEnd
