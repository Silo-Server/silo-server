# Plugin scheduled tasks

Each `scheduled_task.v1` capability of an enabled, non-builtin plugin
installation is one task in the task manager, keyed
`plugin:<installation id>:<capability id>`, unless its row in
`plugin_task_bindings` is disabled. `plugins.TaskRegistry`
(`internal/plugins/task_registry.go`) keeps the task manager in step with
those tables. Every API server runs its own task manager and its own
registry; proxy and transcode nodes run none.

## Schedule

A plugin task runs the first of these:

1. A schedule saved in `task_schedules`. Only an administrator's edit on the
   task page (`PUT /api/v2/admin/tasks/{key}/triggers`, or the frozen v1
   route) saves one.
2. The binding's `trigger`, when the task manager can run it.
3. The manifest's `scheduled_task.default_trigger` (SDK
   `docs/scheduled-task.md`), when the task manager can run it and, for an
   interval, `interval_ms` is at least 60000.
4. One run shortly after the task starts on a server: at server start, and on
   a running server when the task registers (install or enable) or its
   schedule changes to this one.

"Can run" means a type the task manager knows (`interval`, `daily`, `weekly`,
`startup`) with the fields that type reads: a positive interval, an `HH:MM`
time, a weekday from 0 to 6. Anything else, including a malformed or
unknown-type binding trigger, falls through to the next source. Sources 2 and
3 are normalized to the fields their type reads, as `task_schedules` stores
them. Daily and weekly times are in the API server process's time zone.

Sources 2 to 4 are the task's `DefaultTriggers`. A plugin task is a
`taskmanager.DefaultFollowingTask`: the task manager runs its defaults when no
schedule is saved and never saves them, unlike core tasks, whose defaults it
saves the first time they register. So:

- a plugin release that declares or changes `default_trigger`, or a binding
  write that changes the trigger, reaches every task without a saved schedule;
- a task-page edit stands across restarts, plugin updates and binding writes;
- `GET .../tasks/{key}/triggers` on a task with no saved schedule answers its
  defaults at revision zero, and the first `PUT` saves a schedule.

Migration `20261106015045_plugin_tasks_follow_manifest_default` cleared the
two kinds of stored row that predate this rule and would otherwise outrank the
manifest default: binding triggers equal to `{"type":"startup"}`, which the
admin plugin page's switch stored, and saved plugin task schedules of exactly
one startup trigger, which the task manager seeded. The page's switch now
resends only a trigger set through the API.

## Applying changes

`TaskRegistry.Sync` reads the enabled installations, their capabilities and
all task bindings, then registers missing tasks, unregisters tasks that are
gone or disabled, and updates names and defaults in place. For each task it
already had, it calls `TaskManager.ReloadSchedule`, which installs the task's
schedule (a saved one, else its defaults) when it differs from the one the
task runs, so a task-page edit on another server, a changed binding trigger or
a new manifest default applies here. Both sides are compared normalized
(`TriggerConfig.Normalized`), because `task_schedules` keeps `day_of_week`
only on weekly triggers; otherwise a daily trigger carrying a weekday would
look changed on every sync and restart its timer. As after an edit, a
reloaded interval starts from now. A failed read changes nothing, so a
database error never unregisters a task. Unregistering stops the task's
triggers and cancels a run in progress; a saved schedule and the history stay.
A sync never writes a schedule.

Each API server syncs:

- before a plugin lifecycle change on that server returns (a
  `Service.AddLifecycleHook` hook: install, enable, disable, upgrade,
  uninstall, auto-update);
- before a task binding write on that server returns. The write then
  publishes `cache.EventPluginTasksChanged` on `cache.ChannelAdmin`, which only
  task registries read;
- on every `cache.EventPluginTasksChanged` and `cache.EventPluginsChanged`,
  which lifecycle changes on other servers publish;
- every `plugins.TaskSyncInterval` (one minute), and 10 seconds after a
  failed sync, so a server that missed an announcement, a sync that failed, or
  a task-page edit on another server catches up without a restart.

A server that restarts registers from the database at boot, so it sees every
change made while it was down. Binding writes answer `restart_required: false`
on both the v2 and the frozen v1 route; it is true only on a server with no
task manager.

Invariants:

- A task registers on its saved schedule, else the defaults its sync read, so
  it never starts on a trigger that read replaced. Registering resumes an
  interval from the last run, as a restart does.
- An unregistered task never starts a run: its worker refuses runs once
  retired, including a firing its trigger loop took before `Unregister` and a
  manual run that looked the task up before it.
