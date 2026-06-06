UPDATE public.admin_jobs
SET status = 'failed',
    message = CASE
      WHEN message = '' THEN 'Admin job cancelled'
      ELSE message
    END,
    error_message = CASE
      WHEN error_message = '' THEN 'Cancelled status removed by rollback'
      ELSE error_message
    END
WHERE status = 'cancelled';

ALTER TABLE public.admin_jobs
  DROP CONSTRAINT IF EXISTS admin_jobs_status_check;

ALTER TABLE public.admin_jobs
  ADD CONSTRAINT admin_jobs_status_check
  CHECK ((status = ANY (ARRAY[
    'queued'::text,
    'running'::text,
    'completed'::text,
    'failed'::text
  ])));
