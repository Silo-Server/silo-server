# Plugin scheduled tasks

Each `scheduled_task.v1` capability of an enabled, non-builtin plugin
installation is one task in the task manager, keyed
`plugin:<installation id>:<capability id>`, unless its row in
`plugin_task_bindings` is disabled. `plugins.TaskRegistry`
(`internal/plugins/task_registry.go`) builds those tasks when an API server
starts. Every API server runs its own task manager; proxy and transcode nodes
run none. Each scheduled firing still runs on one API server (see "One run per
firing").

## Schedule

A plugin task runs the first of these:

1. A schedule saved in `task_schedules`. Only an administrator's edit on the
   task page (`PUT /api/v2/admin/tasks/{key}/triggers`, or the frozen v1
   route) saves one now; earlier releases also saved a binding's interval,
   daily or weekly trigger there when the task first registered, and those
   rows remain.
2. The binding's `trigger`, when the task manager can run it.
3. The manifest's `scheduled_task.default_trigger` (SDK
   `docs/scheduled-task.md`), when the task manager can run it and, for an
   interval, `interval_ms` is at least 60000.
4. One run shortly after the server starts.

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

- a plugin release that declares or changes `default_trigger` reaches every
  task without a saved schedule, and a binding trigger reaches it likewise;
- a task-page edit stands across restarts, plugin updates and binding writes;
- `GET .../tasks/{key}/triggers` on a task with no saved schedule answers its
  defaults at revision zero, under an ETag that also changes with those
  defaults, and the first `PUT` saves a schedule.

Migration `20261106015045_plugin_tasks_follow_manifest_default` cleared the
two kinds of stored row that predate this rule and would otherwise outrank the
manifest default: binding triggers equal to `{"type":"startup"}`, which the
admin plugin page's switch stored, and saved plugin task schedules of exactly
one startup trigger, which the task manager seeded. A seeded row cannot be
told apart from an administrator who saved exactly "at startup", so the
migration cleared those too: such a task now follows its binding and manifest
default, and still runs once at startup unless the plugin declares a default.
The page's switch now resends only a trigger set through the API.

## Applying changes

The registry reads installations, capabilities and bindings once, at server
start. Installing, enabling, disabling, updating or removing a plugin, and a
binding write, take effect on each API server when it restarts; the binding
write answers `restart_required: true`. A task-page edit applies to the
server that takes it and to the others when they restart.

## One run per firing

Every API server registers every plugin task and arms its triggers, so each
firing happens on every server. A plugin task is a
`taskmanager.ClusterFiringTask`: before its trigger loop runs a firing, the
task manager claims it through `taskmanager.FiringClaims`
(`repository.PgFiringClaims`, table `task_firing_claims`, migration
`20261106022808_add_task_firing_claims`). The server that records the claim
runs the firing; every other server skips it and rearms its triggers from now,
as after a skipped preflight. Manual runs (Run now) never claim. Core tasks do
not claim; those that must not run on two servers at once take an advisory
lock of their own.

A claim is a row keyed by task and slot, inserted under a transaction-scoped
advisory lock on the task, unless the task already has a claim at that slot or
one newer than the firing's window. `taskmanager.FiringSlot` gives both, from
the trigger that fired and the firing time:

| Trigger | Slot | Window |
| --- | --- | --- |
| `daily`, `weekly` | the scheduled instant nearest the firing | none |
| `interval` | firing time truncated to the interval | the interval less a tenth, at most a minute less |
| `startup` | the firing time | `StartupClaimWindow` (10 minutes) |

Invariants:

- Of the servers that fire one scheduled firing of a plugin task, exactly one
  runs it, whether they fire together or seconds apart. The lock serializes
  claims of one task; the row, which outlives the transaction, is what a
  later server finds. An advisory lock alone would let a server that fires
  after the winner's run finished run it again.
- Daily and weekly slots name the scheduled instant, so a late timer, a start
  spread or a server's clock a few seconds off still names the same firing.
  Servers must share a time zone, since times of day are read in the process
  time zone.
- Interval timers on different servers are not aligned: each counts from the
  last completed run it read from history, and a server that lost a claim
  counts from the loss. The window covers timers that straddle a slot
  boundary, so successive runs of an interval task are at least about one
  interval apart, wherever they run. Runs of one task on different servers
  can overlap only if a run lasts longer than its interval.
- A startup firing runs once per cluster start: a server that starts, or
  registers the task, within 10 minutes of another's startup claim skips it.
  A server restarted alone later runs it again.
- A claim that cannot be recorded fails closed: the server skips the firing
  and logs a warning, and the next firing tries again.
- Claims are pruned in the claiming transaction: a task's claims older than
  both its window and eight days.
