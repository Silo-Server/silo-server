-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS public.admin_jobs_active_job_type_idx;

CREATE UNIQUE INDEX admin_jobs_active_job_type_idx
ON public.admin_jobs USING btree (job_type)
WHERE (
    status = ANY (ARRAY['queued'::text, 'running'::text])
    AND job_type = ANY (ARRAY['catalog_export'::text, 'catalog_import'::text])
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.admin_jobs_active_job_type_idx;

CREATE UNIQUE INDEX admin_jobs_active_job_type_idx
ON public.admin_jobs USING btree (job_type)
WHERE (status = ANY (ARRAY['queued'::text, 'running'::text]));
-- +goose StatementEnd
