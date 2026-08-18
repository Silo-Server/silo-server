-- +goose NO TRANSACTION
-- +goose Up
-- Keep provider failures out of the recurring person refresh batch without
-- changing updated_at, which continues to describe actual metadata changes.
ALTER TABLE people
    ADD COLUMN IF NOT EXISTS metadata_refresh_attempted_at timestamptz;

-- The worker orders and filters by this expression every ten minutes. Building
-- concurrently avoids blocking catalog writes on large people tables.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_metadata_refresh_due
ON people (
    GREATEST(updated_at, COALESCE(metadata_refresh_attempted_at, updated_at)),
    id
)
WHERE tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_people_metadata_refresh_due;

ALTER TABLE people
    DROP COLUMN IF EXISTS metadata_refresh_attempted_at;
