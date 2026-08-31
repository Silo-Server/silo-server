-- +goose Up
CREATE INDEX idx_user_personal_collections_image_candidates
    ON user_personal_collections (id)
    WHERE include_in_server_collections = TRUE
      AND collection_type <> 'playlist';

-- +goose Down
DROP INDEX IF EXISTS idx_user_personal_collections_image_candidates;
