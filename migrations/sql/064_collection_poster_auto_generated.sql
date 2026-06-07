-- +goose Up
-- +goose StatementBegin
ALTER TABLE library_collections
    ADD COLUMN IF NOT EXISTS poster_auto_generated BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE library_collections DROP COLUMN IF EXISTS poster_auto_generated;
-- +goose StatementEnd
