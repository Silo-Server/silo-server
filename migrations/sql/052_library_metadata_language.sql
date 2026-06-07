-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_folders ADD COLUMN metadata_language text NOT NULL DEFAULT 'en';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_folders DROP COLUMN metadata_language;
-- +goose StatementEnd
