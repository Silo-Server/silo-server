-- +goose Up
-- +goose StatementBegin
ALTER TABLE user_personal_collections
  ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;

-- Seed existing rows with 0-based positions per user, ordered by created_at.
UPDATE user_personal_collections SET sort_order = sub.pos
FROM (
  SELECT user_id, id,
         ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at, id) - 1 AS pos
  FROM user_personal_collections
) sub
WHERE user_personal_collections.user_id = sub.user_id
  AND user_personal_collections.id = sub.id;

CREATE INDEX idx_user_personal_collections_sort_order
  ON user_personal_collections (user_id, sort_order, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_user_personal_collections_sort_order;
ALTER TABLE user_personal_collections DROP COLUMN sort_order;
-- +goose StatementEnd
