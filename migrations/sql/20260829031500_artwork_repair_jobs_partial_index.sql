-- +goose NO TRANSACTION

-- +goose Up
-- The rebuild-status aggregate counts outstanding repair jobs with
-- "repair_requested AND status IN ('queued', 'running')". Repair rows are a
-- tiny fraction of metadata_image_cache_jobs (~one row per artwork target),
-- so without an index every rebuild-status evaluation — the recovery loop and
-- loss-clearing publications — scans the whole job table. The partial index
-- keeps that count proportional to live repair work.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_metadata_image_cache_jobs_repair_outstanding'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_metadata_image_cache_jobs_repair_outstanding;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_metadata_image_cache_jobs_repair_outstanding
ON public.metadata_image_cache_jobs USING btree (status)
WHERE repair_requested;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_metadata_image_cache_jobs_repair_outstanding;
