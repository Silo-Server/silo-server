-- +goose Up
-- +goose StatementBegin
ALTER TABLE library_collections DROP CONSTRAINT IF EXISTS library_collections_management_mode_check;

ALTER TABLE library_collections ADD CONSTRAINT library_collections_management_mode_check
  CHECK (management_mode = ANY (ARRAY['manual', 'section', 'template_bundle']));

DROP INDEX IF EXISTS idx_library_collections_section_management_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_library_collections_managed_management_key
  ON library_collections (management_key)
  WHERE management_mode <> 'manual' AND management_key <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_library_collections_managed_management_key;

ALTER TABLE library_collections DROP CONSTRAINT IF EXISTS library_collections_management_mode_check;

ALTER TABLE library_collections ADD CONSTRAINT library_collections_management_mode_check
  CHECK (management_mode = ANY (ARRAY['manual', 'section']));

CREATE UNIQUE INDEX IF NOT EXISTS idx_library_collections_section_management_key
  ON library_collections (management_key)
  WHERE management_mode = 'section' AND management_key <> '';
-- +goose StatementEnd
