-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_folders
  ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;

-- Seed existing rows with 0-based positions in current id-ascending order.
UPDATE media_folders SET sort_order = sub.pos
FROM (SELECT id, ROW_NUMBER() OVER (ORDER BY id) - 1 AS pos FROM media_folders) sub
WHERE media_folders.id = sub.id;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_folders DROP COLUMN sort_order;
-- +goose StatementEnd
