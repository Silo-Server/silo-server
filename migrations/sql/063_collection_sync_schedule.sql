-- +goose Up
-- +goose StatementBegin
ALTER TABLE library_collections
    ADD COLUMN IF NOT EXISTS sync_schedule TEXT,
    ADD COLUMN IF NOT EXISTS next_sync_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_library_collections_next_sync_due
    ON library_collections (next_sync_at)
    WHERE sync_schedule IS NOT NULL AND next_sync_at IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_library_collections_next_sync_due;

ALTER TABLE library_collections
    DROP COLUMN IF EXISTS next_sync_at,
    DROP COLUMN IF EXISTS sync_schedule;
-- +goose StatementEnd
