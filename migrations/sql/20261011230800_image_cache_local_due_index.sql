-- +goose NO TRANSACTION

-- +goose Up
-- Local sidecar artwork is drained even while metadata.cache_images is off,
-- when the queue can still hold a large backlog of remote jobs nobody claims.
-- Index the local rows so that drain does not scan the remote ones.
CREATE INDEX CONCURRENTLY IF NOT EXISTS metadata_image_cache_jobs_local_due_idx
    ON metadata_image_cache_jobs (next_attempt_at, id)
    WHERE status = 'queued' AND source_path LIKE 'file://%';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS metadata_image_cache_jobs_local_due_idx;
