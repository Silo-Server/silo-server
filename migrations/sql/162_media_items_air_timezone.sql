-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_items ADD COLUMN IF NOT EXISTS air_timezone text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_items DROP COLUMN IF EXISTS air_timezone;
-- +goose StatementEnd
