-- +goose Up
-- +goose StatementBegin
ALTER TABLE library_collections ADD COLUMN poster_thumbhash TEXT NOT NULL DEFAULT '';
ALTER TABLE library_collections ADD COLUMN backdrop_thumbhash TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE library_collections DROP COLUMN IF EXISTS poster_thumbhash;
ALTER TABLE library_collections DROP COLUMN IF EXISTS backdrop_thumbhash;
-- +goose StatementEnd
