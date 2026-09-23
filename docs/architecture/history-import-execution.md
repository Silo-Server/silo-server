# History import execution

History imports persist a versioned dispatch intent in `history_import_runs`
before reporting acceptance. Administrative intent references source and mapping revisions,
the external user locator, and the original Silo account/profile target. Admin tokens
remain in the source's encrypted storage. Personal intent retains the original target,
optional predefined-source revision, and a run-scoped encrypted credential envelope.
Tokens are decrypted only for the exact running claim generation. A queued run does
not depend on an in-memory provider or wake signal: each node polls for work as well as accepting local wake signals.
Construction does not start recovery or dispatch. Production installs the stable
identity resolver and run observers, then calls `StartBackgroundWork`; activation
is idempotent. Configuration must finish before any persisted run can execute.

Movies and series match the local catalog only through TMDB, IMDb, or TVDB identifiers.
Episodes match through their own identifier or through a series identifier plus season and
episode number. Records without one of these identities remain unmatched; title and year do not
establish media identity. Watch provider sync and webhook sync use the same matching rule.

Emby list queries omit the production year, play count, and last-played date unless
`Fields` names `ProductionYear`, `UserDataPlayCount`, and `UserDataLastPlayedDate`. The
last-played date is what creates history rows and orders imported progress against local
activity, so a missing date would leave re-imports unable to update anything. Emby lists
are read in pages and item lookups in batches. Movie, series, and episode favorites are
imported; Silo has no season favorites, so those are counted in a run warning. A played
multi-episode file (S01E01-E02) marks each episode in its range watched, up to ten; a
partly watched one stays on its first episode because its position cannot be split.

A node reserves local capacity before atomically claiming a queued row. Claims use
`FOR UPDATE SKIP LOCKED` and an incremented generation. Heartbeat, progress, and
terminal writes require the running status and exact generation. New personal runs
use dispatch kind `personal` and version 2; admin runs retain version 1. Older admin claimers selecting only version 1 cannot execute personal
intent. Historical personal rows with no dispatch version retain their legacy
meaning and are not recovered or swept merely because of migration. Generation-zero
legacy execution entrypoints cannot modify durable claims or terminal runs.

Personal authentication finishes before opening the admission transaction. Passwords
are discarded after exchange. The transaction locks and revalidates the account/profile,
the captured predefined source when present, and the account-owned login session when
used. Session consumption, queued intent, and its credential row commit together.
Credentials are stored in `history_import_run_credentials`, keyed only by run ID, with
strict encryption and `secret.RowAAD` binding the payload to that row. Plex account and
server tokens remain separate. Workers never reconstruct authorization from a consumed
session, current source token, or saved password.

Deferred constraints require every active personal run to have its immutable credential.
All terminal transitions erase that credential atomically, including legacy maintenance
and administrative cancellation. Missing or unsupported queued envelopes are quarantined
as safe terminal failures so they cannot block later work. Invalid ciphertext, missing
credentials, or changed execution authority fails closed after claim. A queued job survives
a process exit; a running job with uncertain effects is never automatically replayed.

A failed COMMIT response can be ambiguous. The API advises checking existing imports
before another submission and does not report an uncertain admission as proven rollback.
Submissions have no durable request identity and remain non-retryable automatically.

Source, token, and mapping edits remain available during an import. Workers retain
the original target, validate current configuration revisions before provider
execution and each target operation, and repeat validation on their 15-second
heartbeat. A changed or missing configuration stops the run with a review message;
it never redirects work to the new target. Updating a mapping's import timestamp
does not change its configuration revision. A completed claim updates that timestamp
only if the mapping still matches the captured revision and target.

Mapping targets use `silo_user_id` and `silo_profile_id`. Databases created by the
project's earlier name kept `continuum_*` column names and bootstrap past the
converted migration through their existing `schema_versions` rows; a repair
migration renames those columns in place, preserving row IDs, configuration
revisions, indexes and foreign keys. A database containing both names for either
target column must reconcile the ambiguity before migration; the repair never
chooses a target or merges values. Rolling back the repair keeps the canonical
names because the preceding application version also requires them.

Queued cancellation is immediately terminal. Running cancellation persists a request
and signals a local worker when present. A remote worker observes the request through
validation or heartbeat, stops, and acknowledges the terminal cancellation. An expired
worker with a pending cancellation is reconciled as cancelled. Cancellation does not
undo completed effects or guarantee that an operation already in flight stops before
committing: PostgreSQL queue state and the per-user store are not one transaction.
Configuration changes have the same in-flight limit.

Running jobs with expired heartbeats become terminal failures, never automatic retries.
Some target writes may already have committed before a crash; an administrator can
review the outcome and explicitly create a new run. Legacy queued admin jobs without
reconstructible dispatch metadata fail with an operator-visible explanation. The
continuous legacy orphan sweep applies only to admin-token runs. The unchanged stale-running
heartbeat policy applies to both kinds; its conditional update rechecks freshness after
waiting for a concurrent heartbeat and erases personal credentials only upon terminalization.

Administrative admission serializes on the mapping row and checks all active runs. A database trigger
covers legacy insert paths as well as new admissions; a partial unique index additionally
protects new durable jobs. Existing duplicate active rows remain unchanged and block
new admissions until they reach terminal states. The migration does not delete or select
a winner among historical duplicates. Run targets and dispatch metadata are immutable;
foreign-key deletion may detach a mapping while the retained dispatch locator preserves
source-filtered audit history. Terminal execution state cannot be overwritten.

The admin enqueue lock order is source, then mapping. Personal enqueue locks its
optional source, account/profile pair, and login session in that order. Claims and ordinary progress or
terminal transitions lock the run without acquiring a mapping admission lock. Mapping
configuration edits use the same source-before-mapping order. The admission trigger
performs its active-run lookup after the mapping lock wait; a PostgreSQL regression test
verifies that a waiting READ COMMITTED transaction sees the preceding committed run.

Bulk admission reads at most 201 mapping IDs in stable name/ID order and rejects sources
with more than 200 before creating any runs. Supported batches report every mapping as
accepted, active, or failed. Partial success is explicit; transport failures are not an
invitation to automatically replay the batch.

Canonical source responses redact credentials, query strings, and fragments from
legacy addresses and identify configurations that need review. They do not rewrite
the stored address or redirect a queued worker. An administrator must explicitly
review and save a valid address; new writes reject credential-bearing URLs, queries,
and fragments. The original stored configuration revision still guards that edit.
