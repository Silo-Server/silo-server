-- +goose Up
-- +goose StatementBegin
-- Per-library admin collection groups.
CREATE TABLE library_collection_groups (
    library_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    label TEXT NOT NULL,
    title TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (library_id, label)
);

-- Per-user personal-collection groups.
CREATE TABLE user_collection_groups (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label TEXT NOT NULL,
    title TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, label)
);

ALTER TABLE library_collections
    ADD COLUMN group_label TEXT NOT NULL DEFAULT '';

ALTER TABLE user_personal_collections
    ADD COLUMN group_label TEXT NOT NULL DEFAULT '';

-- Used by ReorderCollections when scoped by group_label; the user_id
-- prefix matches the row-ownership filter on every query path.
CREATE INDEX idx_user_personal_collections_group_order
    ON user_personal_collections (user_id, group_label, sort_order);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_user_personal_collections_group_order;

ALTER TABLE user_personal_collections DROP COLUMN IF EXISTS group_label;
ALTER TABLE library_collections DROP COLUMN IF EXISTS group_label;

DROP TABLE IF EXISTS user_collection_groups;
DROP TABLE IF EXISTS library_collection_groups;
-- +goose StatementEnd
