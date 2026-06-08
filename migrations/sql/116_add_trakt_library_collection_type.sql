-- +goose Up
-- +goose StatementBegin
ALTER TABLE library_collections DROP CONSTRAINT IF EXISTS library_collections_collection_type_check;

ALTER TABLE library_collections ADD CONSTRAINT library_collections_collection_type_check
  CHECK (collection_type = ANY (ARRAY['manual', 'smart', 'mdblist', 'tmdb', 'trakt']));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE library_collections
SET collection_type = 'manual',
    source_config = '{}'::jsonb,
    source_url = '',
    sync_schedule = NULL,
    next_sync_at = NULL,
    updated_at = NOW()
WHERE collection_type = 'trakt';

ALTER TABLE library_collections DROP CONSTRAINT IF EXISTS library_collections_collection_type_check;

ALTER TABLE library_collections ADD CONSTRAINT library_collections_collection_type_check
  CHECK (collection_type = ANY (ARRAY['manual', 'smart', 'mdblist', 'tmdb']));

-- +goose StatementEnd
