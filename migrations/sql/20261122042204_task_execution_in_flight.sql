-- +goose Up
-- Track in-flight task executions with an owner and heartbeat so a process
-- crash leaves a visible failed record instead of dropping the run, without
-- interfering with healthy runs on peer API servers.
ALTER TABLE public.task_executions
    ALTER COLUMN completed_at DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS owner_id TEXT,
    ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_task_executions_running_heartbeat
    ON public.task_executions (heartbeat_at)
    WHERE status = 'running';

-- +goose Down
DROP INDEX IF EXISTS public.idx_task_executions_running_heartbeat;
LOCK TABLE public.task_executions IN ACCESS EXCLUSIVE MODE;
DELETE FROM public.task_executions WHERE status = 'running' OR completed_at IS NULL;
ALTER TABLE public.task_executions
    DROP COLUMN IF EXISTS heartbeat_at,
    DROP COLUMN IF EXISTS owner_id,
    ALTER COLUMN completed_at SET NOT NULL;
