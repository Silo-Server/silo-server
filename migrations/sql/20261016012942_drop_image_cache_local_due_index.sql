-- +goose NO TRANSACTION

-- +goose Up
-- With metadata.cache_images off the image cache claims nothing, local
-- artwork included, so the local-only claim this index served is gone.
DROP INDEX CONCURRENTLY IF EXISTS metadata_image_cache_jobs_local_due_idx;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS metadata_image_cache_jobs_local_due_idx
    ON metadata_image_cache_jobs (next_attempt_at, id)
    WHERE status = 'queued' AND source_path LIKE 'file://%';
