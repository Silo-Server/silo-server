-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_items ADD COLUMN air_time text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_items DROP COLUMN air_time;
-- +goose StatementEnd
