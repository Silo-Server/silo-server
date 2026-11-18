-- +goose Up
-- A plugin task's schedule is now the first of: a schedule an administrator
-- saved on the task page, the binding's trigger, the manifest's default
-- trigger, one run at startup. Two kinds of stored row would outrank the
-- manifest default without anyone having chosen them.

-- The admin plugin page's task switch stored {"type":"startup"} as the
-- binding trigger whenever it saved a binding without one.
UPDATE plugin_task_bindings
SET trigger = '{}'::jsonb, updated_at = NOW()
WHERE trigger = '{"type": "startup"}'::jsonb;

-- The task manager saved every plugin task's default as its schedule the first
-- time the task registered. With no manifest defaults before this change, that
-- default was one startup trigger unless the binding set a trigger. Remove
-- those seeded schedules: exactly one startup trigger with no runtime limit, on
-- a task whose binding sets no interval, daily or weekly trigger (that binding
-- would have seeded its own trigger, so a startup schedule there is an edit).
-- An administrator who saved exactly "at startup" on the task page cannot be
-- told apart from a seed; that task now follows its binding and manifest
-- default, which before a plugin declares a default is still one startup run.
CREATE TEMPORARY TABLE seeded_plugin_task_schedules ON COMMIT DROP AS
SELECT s.task_key
FROM task_schedules s
WHERE s.task_key LIKE 'plugin:%'
  AND (SELECT count(*) FROM task_triggers t WHERE t.task_key = s.task_key) = 1
  AND EXISTS (
      SELECT 1 FROM task_triggers t
      WHERE t.task_key = s.task_key AND t.type = 'startup' AND t.max_runtime IS NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM plugin_task_bindings b
      WHERE 'plugin:' || b.plugin_installation_id || ':' || b.capability_id = s.task_key
        AND b.trigger ->> 'type' IN ('interval', 'daily', 'weekly')
  );
DELETE FROM task_triggers WHERE task_key IN (SELECT task_key FROM seeded_plugin_task_schedules);
DELETE FROM task_schedules WHERE task_key IN (SELECT task_key FROM seeded_plugin_task_schedules);

-- +goose Down
-- Data cleanup only. A task with no saved schedule runs its default, so there
-- is nothing to restore.
SELECT 1;
