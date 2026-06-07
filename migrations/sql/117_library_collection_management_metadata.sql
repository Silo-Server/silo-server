-- +goose Up
-- +goose StatementBegin
ALTER TABLE library_collections
  ADD COLUMN IF NOT EXISTS management_mode TEXT NOT NULL DEFAULT 'manual',
  ADD COLUMN IF NOT EXISTS management_source TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS management_key TEXT NOT NULL DEFAULT '';

ALTER TABLE library_collections DROP CONSTRAINT IF EXISTS library_collections_management_mode_check;

ALTER TABLE library_collections ADD CONSTRAINT library_collections_management_mode_check
  CHECK (management_mode = ANY (ARRAY['manual', 'section']));

CREATE UNIQUE INDEX IF NOT EXISTS idx_library_collections_section_management_key
  ON library_collections (management_key)
  WHERE management_mode = 'section' AND management_key <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_library_collections_section_management_key;

ALTER TABLE library_collections DROP CONSTRAINT IF EXISTS library_collections_management_mode_check;

ALTER TABLE library_collections
  DROP COLUMN IF EXISTS management_key,
  DROP COLUMN IF EXISTS management_source,
  DROP COLUMN IF EXISTS management_mode;
-- +goose StatementEnd
