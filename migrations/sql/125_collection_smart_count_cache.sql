-- +goose Up
-- +goose StatementBegin
ALTER TABLE library_collections
    ADD COLUMN item_count_cached INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN item_count_cached_at TIMESTAMPTZ;

CREATE INDEX idx_library_collections_smart_count_sort
    ON library_collections (collection_type, item_count_cached DESC, title);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_library_collections_smart_count_sort;

ALTER TABLE library_collections
    DROP COLUMN IF EXISTS item_count_cached_at,
    DROP COLUMN IF EXISTS item_count_cached;
-- +goose StatementEnd
