-- +goose Up
-- +goose StatementBegin
-- Allow users to opt their personal collections into the per-library
-- "Collections" tab so a profile can publish a curated shelf alongside
-- the admin/library collections. The default is FALSE so existing
-- collections remain private.

ALTER TABLE user_personal_collections
    ADD COLUMN IF NOT EXISTS include_in_server_collections BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_user_collections_server_visible
    ON user_personal_collections (include_in_server_collections)
    WHERE include_in_server_collections = TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_user_collections_server_visible;

ALTER TABLE user_personal_collections
    DROP COLUMN IF EXISTS include_in_server_collections;
-- +goose StatementEnd
