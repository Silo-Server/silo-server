-- +goose Up
-- +goose StatementBegin
-- Repair historical schema drift where some databases recorded migrations as
-- applied without fully creating their schema changes.

CREATE TABLE IF NOT EXISTS public.task_executions (
    id            BIGSERIAL PRIMARY KEY,
    task_key      TEXT NOT NULL,
    started_at    TIMESTAMPTZ NOT NULL,
    completed_at  TIMESTAMPTZ NOT NULL,
    status        TEXT NOT NULL,
    error_message TEXT,
    result_data   JSONB,
    duration_ms   BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_task_executions_key_completed
    ON public.task_executions (task_key, completed_at DESC);

CREATE TABLE IF NOT EXISTS public.task_triggers (
    id          BIGSERIAL PRIMARY KEY,
    task_key    TEXT NOT NULL,
    type        TEXT NOT NULL,
    interval    BIGINT,
    time_of_day TEXT,
    day_of_week INT,
    max_runtime BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_triggers_key
    ON public.task_triggers (task_key);

ALTER TABLE public.playback_sessions_sync
    ADD COLUMN IF NOT EXISTS transcode_node_url text;
-- +goose StatementEnd
