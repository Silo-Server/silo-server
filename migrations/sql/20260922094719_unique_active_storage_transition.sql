-- +goose Up
CREATE UNIQUE INDEX admin_jobs_active_storage_transition_idx
ON public.admin_jobs USING btree (job_type)
WHERE (
    status = ANY (ARRAY['queued'::text, 'running'::text])
    AND job_type = 'storage_transition'::text
);

-- +goose Down
DROP INDEX IF EXISTS public.admin_jobs_active_storage_transition_idx;
