-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS series_match_queue (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    group_key_version INTEGER NOT NULL,
    content_group_key TEXT NOT NULL,
    first_queued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempted_at TIMESTAMPTZ NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_folder_id, group_key_version, content_group_key)
);

CREATE INDEX IF NOT EXISTS idx_series_match_queue_attempt
    ON series_match_queue (last_attempted_at ASC NULLS FIRST, media_folder_id, group_key_version, content_group_key);

CREATE INDEX IF NOT EXISTS idx_series_match_queue_folder
    ON series_match_queue (media_folder_id, group_key_version, content_group_key);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_series_match_queue_folder;
DROP INDEX IF EXISTS idx_series_match_queue_attempt;
DROP TABLE IF EXISTS series_match_queue;
-- +goose StatementEnd
