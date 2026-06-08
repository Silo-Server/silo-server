-- +goose Up
-- +goose StatementBegin
-- Merge collection_mode into collection_type for library collections.

-- Drop the old constraint first so the backfill can write 'smart'.
ALTER TABLE library_collections DROP CONSTRAINT library_collections_collection_type_check;

-- Backfill: smart mode → smart type.
UPDATE library_collections SET collection_type = 'smart' WHERE collection_mode = 'smart';

-- Drop the mode column.
ALTER TABLE library_collections DROP COLUMN collection_mode;

-- Re-add constraint with 'smart' included.
ALTER TABLE library_collections ADD CONSTRAINT library_collections_collection_type_check
  CHECK (collection_type = ANY (ARRAY['manual', 'smart', 'mdblist', 'tmdb']));

-- Rename collection_mode → collection_type for user personal collections.
ALTER TABLE user_personal_collections RENAME COLUMN collection_mode TO collection_type;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Reverse: rename collection_type back to collection_mode for user personal collections.
ALTER TABLE user_personal_collections RENAME COLUMN collection_type TO collection_mode;

-- Re-add collection_mode column to library collections.
ALTER TABLE library_collections ADD COLUMN collection_mode TEXT NOT NULL DEFAULT 'manual';

-- Backfill: smart type → smart mode.
UPDATE library_collections SET collection_mode = 'smart' WHERE collection_type = 'smart';

-- Revert smart type back to manual.
UPDATE library_collections SET collection_type = 'manual' WHERE collection_type = 'smart';

-- Restore original check constraint without 'smart'.
ALTER TABLE library_collections DROP CONSTRAINT library_collections_collection_type_check;
ALTER TABLE library_collections ADD CONSTRAINT library_collections_collection_type_check
  CHECK (collection_type = ANY (ARRAY['manual', 'mdblist', 'tmdb']));
-- +goose StatementEnd
