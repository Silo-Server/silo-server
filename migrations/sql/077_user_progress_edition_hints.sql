-- +goose Up
-- +goose StatementBegin
ALTER TABLE user_watch_progress
    ADD COLUMN IF NOT EXISTS last_edition_key TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE user_watch_progress
    DROP COLUMN IF EXISTS last_edition_key;
-- +goose StatementEnd
