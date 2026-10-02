-- +goose NO TRANSACTION
-- +goose Up
-- Interactive metadata refreshes requeue, claim, and observe artwork jobs by
-- target. Keep those operations independent of the size of the global cache
-- backlog while preserving due-time order within a target.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'metadata_image_cache_jobs_target_status_due_idx'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.metadata_image_cache_jobs_target_status_due_idx;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS metadata_image_cache_jobs_target_status_due_idx
ON public.metadata_image_cache_jobs (target_content_id, status, next_attempt_at, id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.metadata_image_cache_jobs_target_status_due_idx;
