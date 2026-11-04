# Plugin scheduled tasks

Each `scheduled_task.v1` capability of an enabled, non-builtin plugin
installation is one task in the task manager, keyed
`plugin:<installation id>:<capability id>`, unless its row in
`plugin_task_bindings` is disabled. `plugins.TaskRegistry`
(`internal/plugins/task_registry.go`) keeps the task manager in step with
those tables. Every API server runs its own task manager and its own
registry; proxy and transcode nodes run neither.

## Schedule

A task's default schedule is the first of these the task manager can run:

1. The binding's `trigger`.
2. The manifest's `scheduled_task.default_trigger` (SDK
   `docs/scheduled-task.md`), with `interval_ms` at least 60000.
3. One run at each server start.

"Can run" means a type the task manager knows (`interval`, `daily`, `weekly`,
`startup`) with the fields that type reads: a positive interval, an `HH:MM`
time, a weekday from 0 to 6. Anything else, including a malformed or
unknown-type binding trigger, falls through to the next source.

The default seeds `task_schedules` the first time the task registers, as it
does for core tasks. After that the saved schedule wins, so an administrator's
edit on the task page survives restarts, disabling and enabling the task, and
plugin upgrades that change the manifest default.

One thing replaces the saved schedule: a binding write whose runnable trigger
differs from the one the binding stored before (none for a new row).
`RuntimeConfigStore.UpsertTaskBinding` saves the default the new binding
implies (its trigger, else the manifest default, else startup) in the same
transaction as the binding row, locking that row first so concurrent writes of
one binding each compare against the trigger the previous write stored.
Resending the stored trigger with only `enabled` changed, as the plugin page's
switch does, keeps the saved schedule. A binding for a capability that is not a
scheduled task stores no schedule.

Invariants:

- A binding's schedule change is written once, by the server that takes the
  write, and commits or rolls back with the binding. No server keeps state
  about it in memory, so a restart, a crash after the write, or a server that
  registers the task later all see the same saved schedule.
- Syncs never write a schedule (apart from seeding the default of a task that
  has none). An edit on the task page made after a binding write therefore
  stands on every server until the next binding write changes the trigger.
- A task registers on its saved schedule, which already holds any binding
  change its sync read, so it never starts on a trigger its binding replaced.
  Registering resumes an interval from the last run, as a restart does.
- An unregistered task never starts a run: its worker refuses runs once
  retired, including a firing its trigger loop took before `Unregister` and a
  manual run that looked the task up before it.

## Applying changes

`TaskRegistry.Sync` reads the enabled installations, their capabilities and
all task bindings, then registers missing tasks, unregisters tasks that are
gone or disabled, and updates names and default schedules in place. For each
task it already had, it calls `TaskManager.ReloadSchedule`, which installs the
saved schedule when it differs from the one the task runs, so a schedule saved
on another server, by a binding write or the task page, applies here. As after
an edit, a reloaded interval starts from now. A failed read changes nothing, so
a database error never unregisters a task. Unregistering stops the task's
triggers and cancels a run in progress; its saved schedule and history stay.

Each API server syncs:

- before a plugin lifecycle change on that server returns (a
  `Service.AddLifecycleHook` hook: install, enable, disable, upgrade,
  uninstall, auto-update);
- before a task binding write on that server returns. The write then
  publishes `cache.EventPluginsChanged` on `cache.ChannelAdmin`;
- on every `cache.EventPluginsChanged`, which lifecycle changes on other
  servers also publish;
- every `plugins.TaskSyncInterval` (one minute), and 10 seconds after a
  failed sync, so a server that missed an announcement, a sync that failed, or
  a task-page edit on another server catches up without a restart.

A server that restarts registers from the database at boot, so it sees every
change made while it was down.
