ALTER TABLE public.admin_jobs
  DROP CONSTRAINT IF EXISTS admin_jobs_status_check;

ALTER TABLE public.admin_jobs
  ADD CONSTRAINT admin_jobs_status_check
  CHECK ((status = ANY (ARRAY[
    'queued'::text,
    'running'::text,
    'completed'::text,
    'failed'::text,
    'cancelled'::text
  ])));
