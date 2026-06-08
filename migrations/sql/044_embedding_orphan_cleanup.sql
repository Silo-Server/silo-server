-- +goose Up
-- +goose StatementBegin
-- Remove orphaned embeddings for items that no longer exist.
DELETE FROM media_item_embeddings
WHERE media_item_id NOT IN (SELECT content_id FROM media_items);

-- Add foreign key so future deletes cascade automatically.
ALTER TABLE media_item_embeddings
  ADD CONSTRAINT media_item_embeddings_media_item_id_fkey
  FOREIGN KEY (media_item_id) REFERENCES media_items(content_id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_item_embeddings
  DROP CONSTRAINT IF EXISTS media_item_embeddings_media_item_id_fkey;
-- +goose StatementEnd
