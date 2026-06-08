-- +goose Up
-- +goose StatementBegin
-- Allow users to attach a custom poster to their personal collections,
-- mirroring the admin/library collection artwork pipeline. Both columns
-- default to '' so existing rows remain unchanged.

ALTER TABLE user_personal_collections
    ADD COLUMN IF NOT EXISTS poster_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS poster_thumbhash TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE user_personal_collections
    DROP COLUMN IF EXISTS poster_thumbhash,
    DROP COLUMN IF EXISTS poster_url;
-- +goose StatementEnd
