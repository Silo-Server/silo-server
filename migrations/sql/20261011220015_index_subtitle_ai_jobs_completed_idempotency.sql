-- +goose NO TRANSACTION

-- +goose Up
-- A translate request looks up a completed job with the same idempotency key
-- so a finished translation is reused instead of billed again. The existing
-- key index covers only pending and running jobs, and job rows are never
-- deleted, so without this index every request scans the job history.

-- A failed concurrent build can leave an invalid index. Keep retry cleanup
-- outside a transaction so it cannot take a blocking ordinary index-drop lock.
DROP INDEX CONCURRENTLY IF EXISTS public.subtitle_ai_jobs_completed_idempotency_idx;

CREATE INDEX CONCURRENTLY subtitle_ai_jobs_completed_idempotency_idx
    ON public.subtitle_ai_jobs (idempotency_key, updated_at DESC)
    WHERE status = 'completed';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.subtitle_ai_jobs_completed_idempotency_idx;
