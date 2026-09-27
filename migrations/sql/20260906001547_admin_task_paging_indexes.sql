-- +goose Up
CREATE INDEX admin_jobs_page_idx ON admin_jobs (requested_at DESC, id DESC);
CREATE INDEX admin_jobs_kind_page_idx ON admin_jobs (job_type, requested_at DESC, id DESC);
CREATE INDEX task_executions_page_idx ON task_executions (task_key, completed_at DESC, id DESC);

-- +goose Down
DROP INDEX task_executions_page_idx;
DROP INDEX admin_jobs_kind_page_idx;
DROP INDEX admin_jobs_page_idx;
