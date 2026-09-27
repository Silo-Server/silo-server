-- +goose Up
-- A schedule exists even when it has no triggers. The parent serializes legacy
-- replacements and guarded edits, and keeps an empty schedule across restarts.
CREATE TABLE task_schedules (
    task_key TEXT PRIMARY KEY,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0)
);
INSERT INTO task_schedules (task_key) SELECT DISTINCT task_key FROM task_triggers;

-- +goose Down
DROP TABLE task_schedules;
