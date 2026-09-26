# Administrator tasks and jobs

The v2 administrator task surface uses generated web bindings. Apple and Android
have no task or administrator-job HTTP calls in their checked source trees; the
migration inventory also records only web consumers. Jellyfin compatibility has
no equivalent management surface. Legacy routes remain available during migration.

## Execution and schedules

`GET /api/v2/admin/tasks` returns the finite registered task list in `items`.
By default it omits hidden tasks: queue workers, pollers, and repairs that run
without an administrator, such as `match_media` or `cache_metadata_images`. It
also omits tasks that serve one library kind, such as `sync_ebook_metadata`,
while no library of that kind exists. `include_hidden=true` returns every
registered task. The frozen v1 list applies the hidden flags only, not library
scoping. `GET /api/v2/admin/tasks/{key}` reads runtime state on the
responding process for any registered task, hidden or not. Both require
acting-administrator access, as do task mutations and history.

`POST /api/v2/admin/tasks/{key}/run` reserves the local worker before returning
HTTP 200 with `execution_scope: "process"`. It starts execution asynchronously,
but does not create a durable job or promise execution after a process failure.
Concurrent local starts conflict. Another server has its own worker state.
`POST /api/v2/admin/tasks/{key}/cancel` requests cancellation of the current local
execution; completed effects remain. Neither operation automatically retries.

The `refresh_all_library_metadata` task runs a `full` library metadata refresh
(see [libraries-api.md](libraries-api.md)) for each enabled library in turn,
inside the task worker, with the same six-hour limit per library as a refresh
job. It is manual-only: interval schedules are timed per process, so a cluster
could start a second full refresh right after the first. Unlike a library
refresh job, it creates no admin job, so it has no retained job record, no
recovery after a process failure, and cancels as a whole on the server running
it. A library with a refresh job queued or running, or another refresh holding
its per-library lock, is skipped. A PostgreSQL advisory lock allows one run
across all servers; a run that finds it held fails without refreshing anything.

`database_maintenance` runs the routine retention sweeps in turn, daily at 05:00
by default: processed search index events, activity log, task history, expired
login sessions, policy decision log, and notifications. The log steps also
create upcoming partitions, which startup creates as well. Every server fires
the same trigger, so a PostgreSQL advisory lock lets one server run the steps;
the others record a completed run that did nothing. Each step keeps its
own retention settings. A failing step does not stop the later ones; the run
fails if any step failed. Its history entries carry a `steps` array with each
step's `key`, `name`, and `status`; error text stays in the server log, like
other task failures.
Operational log and client diagnostics cleanup stay separate tasks because
their caps need a 15-minute cadence.

`bulk_metadata_enrichment` looks movies and series up with enrichment-only
metadata providers that batch their requests, such as MDBList, many titles at a
time. Only providers that declare `bulk_lookup_limit` take part (see
[metadata-enrichment-providers.md](architecture/metadata-enrichment-providers.md)).
It runs hourly and skips runs with nothing to look up. A PostgreSQL advisory
lock lets one server run it; the others record a completed run that did
nothing. It records each answer as it goes, so a canceled run, a restart or a
provider's spent quota loses no work, and the next run continues with the items
still missing an answer. A run the quota stopped still completes successfully.
The server log records each provider's found, empty and failed counts, and why
its turn ended early, such as a spent quota.

Manual-only tasks (`manual_only: true`) are repair and one-off tools. They
reject schedules, and a schedule saved before a task became manual-only is
ignored at startup. The web page lists them in a separate "On demand" group.

`GET /api/v2/admin/tasks/{key}/triggers` reads persisted schedule configuration
and a strong caller-bound ETag. `PUT` on that resource requires `If-Match` and a
`triggers` array, including an empty array to disable scheduling. The database
compares the original revision under the schedule parent lock. Legacy writers
use that same lock and advance the revision. A mismatch returns 412 with the
observed validator, without rereading or retrying the write. An empty schedule
remains configured across restarts rather than restoring task defaults.

Successful edits apply to the responding process. Other running processes load
the persisted configuration when they restart. Runtime state and persisted
schedule state are separate reads. The web editor captures the persisted schedule
when editing begins, keeps the draft and original validator after conflicts, and
requires explicit review and revision adoption before another submission. It
disables both mutation retries and authentication-refresh replay. The inherited
`max_runtime_ms` setting is stored but is not enforced by the task runner; the
editor labels that limitation rather than promising a timeout.

## History and metrics

`GET /api/v2/admin/tasks/{key}/history` returns persisted executions in an `items`
envelope with explicit `page` state. The default limit is 20 and maximum 200.
Database reads fetch at most limit plus one rows ordered by completed time and
ID descending. Signed cursors bind the task, account, acting profile, and order.
New completions appear after restarting history; loaded pages do not silently
expand into a whole-history read. Live last-execution summaries have no saved ID;
persisted history entries have opaque string IDs.

`GET /api/v2/admin/tasks/refresh_metadata/metrics` returns typed queue counters
and at most ten entries per sample list. Other tasks return 404. Raw diagnostic
errors are replaced with safe failure summaries. Task history does not expose
arbitrary internal result JSON; marker contribution counts have a typed result.
Detailed operational diagnostics remain available through their existing surface.

## Retained jobs

`GET /api/v2/admin/jobs` lists retained jobs with an optional `kind` filter,
default limit 20, maximum 200, and signed `(requested_at, id)` cursor ordering.
The database fetches at most limit plus one rows. Cursors bind the filter,
account, acting profile, and operation. The web exposes explicit continuation
and restart controls for job histories.

`GET /api/v2/admin/jobs/{id}` returns a safe typed job projection. Administrators
may read all retained jobs; other accounts may read only their own item-refresh
job. Hidden and missing jobs both return 404. Nonterminal responses supply
`Retry-After: 5`. The projection keeps library identities, typed result counts,
and authorized catalog artifact links, while excluding raw request documents,
storage keys, file paths, and internal error text. Artifact links can expire and
should be refreshed from the job resource.

Storage-transition jobs include `storage_transition_result` throughout their
lifecycle. Its `phase` is a fixed status value, `verified_objects` counts
objects whose destination content was checked in the current copy pass, and
`failure_category` classifies a failed phase without exposing provider errors,
bucket names, object keys, or local paths. The count can restart when an
interrupted pass resumes. A completed copy reports `restart_pending` until the
new storage is active after restart. The job's raw message and error remain
available only to internal diagnostics. Legacy administrator-job responses and
the jobs realtime channel use the same safe storage-transition receipt,
including the websocket snapshot; they omit the raw request and result
documents for this job type.

The existing `POST /api/v2/library-jobs/{job_id}/cancel` contract remains intact.
`POST /api/v2/admin/jobs/{id}/cancel` (`cancelAdminJob`) requests cancellation
for storage-transition jobs. Library refresh cancellation uses its separate
library-job endpoint above. Cancellation retains completed effects and verified
storage-copy checkpoints. An accepted cancellation returns `202`; an already
canceled job returns `200`; a succeeded or failed job returns
`409 job_not_cancelable`. A queued storage-transition cancellation also releases
its staged target so a later transition can choose a different destination. The
administrator task section does not add a generic durable scheduler or change
the retention and dispatch guarantees of existing job owners.
