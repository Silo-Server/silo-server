-- +goose Up
-- +goose StatementBegin
-- Execution history for completed task runs.
CREATE TABLE task_executions (
    id            BIGSERIAL PRIMARY KEY,
    task_key      TEXT NOT NULL,
    started_at    TIMESTAMPTZ NOT NULL,
    completed_at  TIMESTAMPTZ NOT NULL,
    status        TEXT NOT NULL,          -- 'completed', 'failed', 'cancelled'
    error_message TEXT,
    result_data   JSONB,                  -- task-specific summary
    duration_ms   BIGINT NOT NULL
);

CREATE INDEX idx_task_executions_key_completed ON task_executions (task_key, completed_at DESC);

-- User-configured trigger overrides.
CREATE TABLE task_triggers (
    id          BIGSERIAL PRIMARY KEY,
    task_key    TEXT NOT NULL,
    type        TEXT NOT NULL,             -- 'interval', 'daily', 'weekly', 'startup'
    interval    BIGINT,                    -- milliseconds, for interval type
    time_of_day TEXT,                      -- "HH:MM", for daily/weekly
    day_of_week INT,                       -- 0-6, for weekly
    max_runtime BIGINT,                    -- milliseconds, optional
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_task_triggers_key ON task_triggers (task_key);
-- +goose StatementEnd
