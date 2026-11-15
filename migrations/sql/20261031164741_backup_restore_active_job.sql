-- +goose Up
-- One restore at a time: a second request answers 409 with the running job,
-- instead of queueing behind the backup lock the first one holds.
CREATE UNIQUE INDEX admin_jobs_active_backup_restore_idx
ON public.admin_jobs USING btree (job_type)
WHERE (
    status = ANY (ARRAY['queued'::text, 'running'::text])
    AND job_type = 'backup_restore'::text
);

-- +goose Down
DROP INDEX IF EXISTS public.admin_jobs_active_backup_restore_idx;
