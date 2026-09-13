-- +goose Up
-- A row records that the schedule was initialized or explicitly saved, even
-- when task_triggers has no rows. Existing nonempty schedules remain intact.
CREATE TABLE task_trigger_sets (
    task_key TEXT PRIMARY KEY
);

INSERT INTO task_trigger_sets (task_key)
SELECT DISTINCT task_key FROM task_triggers;

-- +goose Down
-- Trigger rows are retained; older servers cannot remember cleared schedules.
DROP TABLE task_trigger_sets;
