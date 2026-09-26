-- +goose NO TRANSACTION

-- +goose Up
-- Concurrent builds can leave an invalid index after interruption; rebuild on retry.
DROP INDEX CONCURRENTLY IF EXISTS idx_user_personal_collections_image_candidates;

CREATE INDEX CONCURRENTLY idx_user_personal_collections_image_candidates
    ON user_personal_collections (id)
    WHERE include_in_server_collections = TRUE
      AND collection_type <> 'playlist';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_user_personal_collections_image_candidates;
