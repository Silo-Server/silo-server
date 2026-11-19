# Plugin scheduled tasks

Each `scheduled_task.v1` capability of an enabled, non-builtin plugin
installation is one task in the task manager, keyed
`plugin:<installation id>:<capability id>`, unless its row in
`plugin_task_bindings` is disabled. `plugins.TaskRegistry`
(`internal/plugins/task_registry.go`) keeps the task manager in step with
those tables. Every API server runs its own task manager and its own
registry; proxy and transcode nodes run none. Each scheduled firing still runs
on one API server (see "One run per firing").

## Schedule

A plugin task runs the first of these:

1. A schedule saved in `task_schedules`. Only an administrator's edit on the
   task page (`PUT /api/v2/admin/tasks/{key}/triggers`) saves one now;
   earlier releases also saved a binding's interval, daily or weekly trigger
   there when the task first registered, and those rows remain.
2. The binding's `trigger`, when the task manager can run it.
3. The manifest's `scheduled_task.default_trigger` (SDK
   `docs/scheduled-task.md`), when the task manager can run it and, for an
   interval, `interval_ms` is at least 60000.
4. One run shortly after the task starts on a server: at server start, and on
   a running server when the task registers (install or enable) or its
   schedule changes to this one. That run claims its firing like any other,
   so it is skipped within 10 minutes of the task's last claim (see "One run
   per firing").

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
  defaults at revision zero, under an ETag that also changes with those
  defaults, and the first `PUT` saves a schedule.

Migration `20261106015045_plugin_tasks_follow_manifest_default` cleared the
two kinds of stored row that predate this rule and would otherwise outrank the
manifest default: binding triggers equal to `{"type":"startup"}`, which the
admin plugin page's switch stored, and saved plugin task schedules of exactly
one startup trigger, which the task manager seeded. It cleared a schedule only
when all of these held: the schedule was one startup trigger with no runtime
limit, its revision was 2 or lower, and its binding set no interval, daily or
weekly trigger. A seed leaves revision 2, and every save adds one, so a
startup schedule an administrator saved after such a seed stayed. Two edits
still look like a seed and were cleared: one saved before schedule revisions
existed (migration `20260906000622`), and the first save since then on a
schedule that predates revisions, which that migration set to revision 1.
Such a task now follows its binding and manifest default, and still runs once
at startup unless the plugin declares a default.
The page's switch now resends only a trigger set through the API.

## Applying changes

`TaskRegistry.Sync` reads enabled installations that declare scheduled
tasks, their capabilities and task bindings. It registers missing tasks,
unregisters tasks that are gone or disabled, and updates names and defaults
in place. For each task it
already had, it calls `TaskManager.ReloadSchedule`, which installs the task's
schedule (a saved one, else its defaults) when it differs from the one the
task runs, so a task-page edit on another server, a changed binding trigger or
a new manifest default applies here. Both sides are compared normalized
(`TriggerConfig.Normalized`), because `task_schedules` keeps `day_of_week`
only on weekly triggers; otherwise a daily trigger carrying a weekday would
look changed on every sync and restart its timer. When a task is idle, a
reloaded interval starts from now. A schedule installed during a run arms
when the run finishes. If the registry scan fails, it keeps the existing
tasks. A new task whose schedule cannot be
read is not registered (`TaskManager.RegisterLive`) and fails the sync, so the
retry registers it. Unregistering stops the task's
triggers and cancels a run in progress. Registration of the same key waits
for the local execution to exit. A saved schedule and the history stay.
A scheduled RPC reads fresh installation state before resolving the plugin client
so a replica can use a release it just learned about.
A sync never writes a schedule.

Each API server syncs:

- before a plugin lifecycle change on that server returns (a
  `Service.AddLifecycleHook` hook: install, enable, disable, upgrade,
  uninstall, auto-update);
- before a task binding write on that server returns. The write then
  publishes `cache.EventPluginTasksChanged` on `cache.ChannelAdmin`. It is
  its own event type, so listeners that rebuild on `plugins_changed`, such as
  proxy nodes and plugin event dispatchers, ignore it;
- on every `cache.EventPluginTasksChanged` and `cache.EventPluginsChanged`,
  which lifecycle changes on other servers publish;
- every `plugins.TaskSyncInterval` (one minute), and 10 seconds after a
  failed sync, so a server that missed an announcement, a sync that failed, or
  a task-page edit on another server catches up without a restart;
- right after the task manager starts.

A server that restarts registers from the database at boot, so it sees every
change made while it was down. At boot plugin tasks register before the task
manager starts, and `TaskManager.Start` only logs a schedule it can't read,
leaving that task idle; the sync right after `Start` reloads every plugin
task's schedule, and if the read still fails, the 10-second retry follows. Binding writes answer `restart_required: false`;
it is true only on a server with no task manager.

Invariants:

- A task registers on its saved schedule, else the defaults its sync read, so
  it never starts on a trigger that read replaced. Registering resumes an
  interval from the last run, as a restart does.
- A retired worker refuses new runs and schedule writes. A claim that
  completed before retirement belongs to a reserved run that retirement can cancel.

## One run per firing

Every API server registers every plugin task and arms its triggers, so each
firing happens on every server. A plugin task is a
`taskmanager.ClusterFiringTask`: before its trigger loop runs a firing, the
task manager claims it through `taskmanager.FiringClaims`
(`repository.PgFiringClaims`, table `task_firing_claims`, migrations
`20261106022808_add_task_firing_claims`,
`20261118193342_add_task_firing_claim_due_at` and
`20261106153043_add_task_firing_claim_expiry`). The server that records the claim
runs the firing; every other server skips it and rearms its triggers from now,
as after a skipped preflight. Manual runs (Run now) never claim. Core tasks do
not claim; those that must not run on two servers at once take an advisory
lock of their own.

A claim is a row keyed by task and slot, inserted under a transaction-scoped
advisory lock on the task, unless the task already has a claim at that slot or
one taken within the firing's window of when the firing was due, or of now,
or one whose own firing was due within that window of this one's (each claim
records its due time, so a winner that claimed late still covers the firing
it served).
`taskmanager.FiringSlot` gives both, from the trigger that fired and the time
its timer was armed for (the wake-up time for a startup trigger):

| Trigger | Slot | Window |
| --- | --- | --- |
| `daily`, `weekly` | the scheduled instant nearest the firing | none |
| `interval` | firing time truncated to the interval | the interval less a tenth, at most a minute less |
| `startup` | the firing time | `StartupClaimWindow` (10 minutes) |

Invariants:

- Of the servers that fire one scheduled firing of a plugin task, at most one
  runs it, whether they fire together or seconds apart. Usually that is
  exactly one. A worker checks its schedule and availability before it
  claims. A server that stops after claiming can still miss that firing.
  The lock serializes
  claims of one task; the row, which outlives the transaction, is what a
  later server finds. An advisory lock alone would let a server that fires
  after the winner's run finished run it again.
- Daily and weekly slots name the scheduled instant nearest the time the
  timer was armed for, so a late timer, a start spread or a server's clock a
  few seconds off still names the same firing.
  Servers must share a time zone, since times of day are read in the process
  time zone.
- Interval timers on different servers are not aligned: each counts from the
  last completed run it read from history, and a server that lost a claim
  counts from the loss. The window covers timers that straddle a slot
  boundary, so successive runs of an interval task are at least about one
  interval apart, wherever they run. Runs of one task on different servers
  can overlap only if a run lasts longer than the window (the interval less
  up to a minute).
- A startup firing runs once per cluster start: a server that starts, or
  registers the task, within 10 minutes of any claim of the task skips it.
  That claim can come from any of the task's triggers and from any process,
  including the previous process of a single-server install, so restarting a
  one-server install within 10 minutes of the task's last claim skips its
  startup run. A server restarted later runs it again.
- A server that loses a claim reads the task's latest completed run from
  history, so its last execution trails the cluster by at most one firing
  (the winner's run may still be going) rather than staying at what it read
  when it started.
- A claim that cannot be recorded fails closed: the server skips the firing
  and logs a warning, and the next firing tries again. So does a firing with
  no slot every server would agree on, such as a malformed time of day, an
  interval that isn't positive.
- Each claim records its expiry, the longer of its window and eight days
  after it was taken. The transaction that takes a claim prunes expired claims
  of all tasks when no other claim is pruning (a global advisory lock, tried
  without waiting, so two prunes never deadlock), so a task that stopped firing leaves no claims behind, and a
  short-interval task never prunes a long-interval task's claim inside that
  claim's window.
