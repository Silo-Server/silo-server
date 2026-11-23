-- +goose Up
-- One row per task run in progress. A run's server renews heartbeat_at while
-- it runs and removes the row when it records the run in task_executions. Any
-- server turns a row whose heartbeat expired into a failed task_executions
-- entry, so a run cut off by a crash still appears in task history.
-- task_executions itself keeps only finished runs.
CREATE TABLE public.task_execution_leases (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_key     TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL,
    heartbeat_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS public.task_execution_leases;
