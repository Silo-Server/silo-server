-- +goose Up
-- +goose StatementBegin
ALTER TABLE series_root_match_queue
    ADD COLUMN IF NOT EXISTS available_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

DROP INDEX IF EXISTS idx_series_root_match_queue_attempt;

CREATE INDEX IF NOT EXISTS idx_series_root_match_queue_available
    ON series_root_match_queue (available_at, last_attempted_at, media_folder_id, observed_root_path);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_series_root_match_queue_available;

CREATE INDEX IF NOT EXISTS idx_series_root_match_queue_attempt
    ON series_root_match_queue (last_attempted_at ASC NULLS FIRST, media_folder_id, observed_root_path);

ALTER TABLE series_root_match_queue
    DROP COLUMN IF EXISTS available_at;
-- +goose StatementEnd
