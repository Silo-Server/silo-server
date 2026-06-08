-- +goose Up
-- +goose StatementBegin
CREATE UNIQUE INDEX admin_jobs_active_library_refresh_idx
ON public.admin_jobs USING btree (job_type, ((request_payload->>'library_id')))
WHERE (
    status = ANY (ARRAY['queued'::text, 'running'::text])
    AND job_type = 'library_refresh'::text
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.admin_jobs_active_library_refresh_idx;
-- +goose StatementEnd
