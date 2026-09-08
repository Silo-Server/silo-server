# Playback attempt reservation authority

`planstore.Postgres` implements `AuthoritativePlanStoreV3`, an extension of the
existing `PlanStoreV3`. It reserves and commits the existing
`playback_v3_attempts` row. The legacy memory store does not claim distributed
fencing. Lifecycle handlers do not yet call this extension.

A reservation binds the attempt ID to its account, profile, requested file, and
existing request digest. The digest is supplied by the protocol boundary; the
store never derives it from reserialized `NormalizedRequest`. The original
normalized request and retention deadline remain unchanged by publication.

Each reservation has a process-boot UUID, row-incarnation UUID, increasing epoch,
database-clock lease, and state. PostgreSQL generates a fresh incarnation on each
insert; it remains stable across takeovers of that row. Every owner/epoch mutation
also compares this incarnation, so retention cleanup and same-boot reuse of an
attempt ID cannot make a delayed old mutation match a new reservation. `preparing` means transport preparation may proceed for the winning
caller. Concurrent callers receive `Owned=false`, even if they supply the same
boot UUID. An expired preparing reservation that has never issued a grant can be reclaimed
with a higher epoch; its uncommitted staged route is cleared. A reservation that
has issued any grant must drain instead.
An active attempt cannot be reclaimed through this operation.

Publication compares the incarnation, owner, epoch, live lease, identity, digest, and preparing
state. It stores the existing decision response and recipe atomically and changes
the state to `active` or `terminal`. Stop changes preparing or active state to a
retained `stopped` tombstone. A stopped attempt cannot publish again. Retries read
the same retained record and its state; they must not blindly replay the playable
response of a stopped record.

Renewal, publication, and stop acquire a row lock in a short transaction before
evaluating expiry with the database clock. No transaction spans a transport call.
A blocked operation cannot renew an expired lease using a timestamp captured
before it acquired the lock. Client-supplied authority timestamps are informational.
Leases never extend past the database retention deadline. Cleanup uses the database
clock for authority rows; after retention and cleanup, replay identity is no
longer retained.

Rows created by the legacy path have state `legacy`. Its readers omit preparing
and stopped records. Its replan writer cannot update an authority-owned row. The
existing replan lease and base-revision CAS remain the replan mechanism; there is
no parallel revision journal. Migration rollback refuses to remove authority
columns while reserved attempts or tombstones remain.

These operations fence database publication only. They do not revoke worker
grants, stop an already-authorized response, or make a PostgreSQL check atomic with
a SQLite personal-progress write. Before lifecycle activation, delivery must
fence execution and long responses, and progress/stop must commit their sequence,
winning state, and ownership check in the selected personal-state store. Active
owner takeover, lease-based worker execution, and coordinated deployment behavior
remain separate acceptance requirements.


## Durable grants and drain

`GrantPlanStoreV3` extends the same store. Grant issuance requires an explicit
`AttemptGrantPolicyV3` maximum duration; the ordinary Postgres constructor keeps
issuance disabled. Test fixtures use five seconds, which is not a protocol or
production tuning promise.

`StageAttemptRoute` freezes the existing plan and executable recipe with their
session, transport, execution-node, and egress-node identities. Repeating the same
binding is allowed; changing it is rejected. Node zero denotes the owning API's
local role. Publication must match the staged binding. Execute grants may use a
preparing or active route; serve grants require the committed active route.
Requests must match the session, plan, transport, purpose and appropriate node.
Every operation also compares the incarnation, boot identity and epoch.

Issuance locks the row before reading database time. The grant expires no later
than the requested duration, configured maximum, owner lease, or retention.
The row's maximum issued deadline is committed before the response is returned.
A lost response still counts; a later shorter grant cannot lower that maximum.
Renewal does not shorten the owner lease beneath already-issued grants.

`BeginAttemptDrain` forbids further issuance and records a stable deadline no
earlier than the maximum issued grant. It can run after owner-lease expiry with
the same fence. `CompleteAttemptDrain` marks the record stopped only once that
database deadline has elapsed. Direct stop cannot bypass an outstanding grant.
These are durable state transitions, not evidence that HTTP responses or FFmpeg
have stopped. They create no successor or active-takeover path.

Runtime recovery must isolate output by incarnation, epoch, and executor
generation, publish immutable recipe locators under authority CAS, and fence
serving and shared-state writes. Obsolete computation can overlap if it cannot
affect the authoritative successor. Positive exit acknowledgement must not be an
indefinite prerequisite for recovery from a dead worker. Those runtime changes
and selected-store progress fencing remain separate from this storage checkpoint.

## Executor output and recipe namespace foundation

Staged routes and grant requests bind an `ExecutorNamespaceV3`: the row
incarnation, epoch, and a separately supplied executor UUID. A replacement writer
requires a fresh executor binding. This checkpoint does not add the lifecycle
operation that replaces a staged binding.

Bound HLS output resolves beneath the configured transcode root as
`_authority/<incarnation>/<epoch>/<executor UUID>`. Starting a process exclusively
claims that name before creating its output leaf. The claim survives process
failure and leaf cleanup, preventing another process from reusing the name.
Reconstruction can reuse an existing matching runtime or launch an unclaimed
bound generation; it cannot respawn a consumed generation. Legacy restart rejects
bound runtimes before stopping them. Cleanup removes only the bound leaf, and the
legacy orphan sweep excludes the authority subtree. Claim retirement needs a
separate authority-aware retention policy.

Redis stores bound recipes under generation-specific keys with atomic
put-if-absent semantics. Identical replay preserves expiry; conflicting bytes are
rejected. A locator includes the namespace and the digest of the full versioned
recipe. Reads verify both; deletion compares the exact stored bytes. Bound cards
cannot use legacy session-key storage or reconstruction fallback.

`PublishAttemptRecipeLocator` publishes that descriptor in PostgreSQL after the
immutable Redis write. It compares the current live authority, staged executor,
and expected locator in one update. Initial publication expects no locator; a
confirmed locator can be replayed unchanged. A stale publisher can leave unused
Redis bytes but cannot replace the current pointer. Preparing reclamation clears
the old locator. A locator alone grants no execution or serving permission.

Worker reconstruction has an explicit current-recipe resolver seam, unwired by
default. Bound reads check their signed namespace against the runtime. Legacy
stop and progressive remux reject bound runtimes or tokens because they do not
yet carry the required authority operations. Replacement lifecycle activation and production dependency configuration remain
required before these foundations can serve authority-owned playback. Runtime
enforcement and metadata checks are described below.


## Runtime grant enforcement

`RuntimeGrantV3` converts a database grant into a cancellable runtime lifetime.
Its policy explicitly supplies maximum duration, safety margin, renewal lead,
and watchdog interval. The watchdog interval must be shorter than both the
safety margin and renewal lead. Invalid or missing configuration refuses bound work.
The local deadline is acquisition request-start plus `NotAfter - IssuedAt`, less
the safety margin. Charging the entire round trip avoids depending on matching
worker and database wall clocks. Late replies, invalid bindings, excessive
intervals, and deadlines beyond the returned owner lease are rejected.

Elapsed time must include suspend. Linux uses `CLOCK_BOOTTIME`; Darwin uses
`CLOCK_MONOTONIC_RAW`. Other platforms refuse configuration. Every guarded
operation checks that clock; errors, backwards readings, and expiry permanently
cancel the lifetime. Timers only schedule checks. Renewal is serialized and
bounded, with an independent expiry watchdog while a request is blocked. A
failed renewal or canceled lifetime cannot be revived by a delayed response.
Execute grants admit preparing or active routes; serve grants require active.

The configured margin must cover bounded clock-rate and dispatch uncertainty.
This assumes the database clock does not jump forward beyond that bound during
a live grant. Arbitrary clock steps or scheduling stalls cannot yield a strict
real-time stop guarantee in userspace. On resume, reads recheck elapsed time;
obsolete compute can briefly persist until cancellation is scheduled, but its
output remains isolated. Bytes already accepted by the kernel before expiry
cannot be recalled. Drain waits for the durable grant bound, not an indefinite
exit acknowledgement from a dead worker.

Bound `StartTranscode` acquires an execute grant before preflight or output
claims, checks it again before launch, and parents FFmpeg to its context. Request
cancellation controls admission; adopted execution outlives that request only
while the grant remains valid. Process exit and failed startup close the grant.
`Session.Executor` preserves the binding without a runtime. Metadata fastpaths
require an exact reference and a fresh serve check; that check alone does not
authorize a subsequent long response. Bound manager reconstruction requires an
explicit authoritative recipe resolver and uses its returned immutable recipe.

`planstore.ExecutorRuntime` supplies concrete grant and recipe callbacks using
the configured node identity, PostgreSQL authority and locator, and immutable
Redis recipe reads. The incarnation lookup has a unique index. Neither a lookup
nor a recipe grants execution: issuance rechecks the live row under its lock.
Constructing the adapter does not activate handlers or renew owner leases.

Worker manifest, segment, and acknowledgement responses acquire separate serve
grants. The response wrapper checks before headers and every body write, caps
socket write deadlines by remaining validity, flushes under that deadline, and
interrupts blocked writes on cancellation. Unsupported deadline writers refuse
bound delivery. Disconnect closes the response grant independently of execution.

The explicitly configured v2 adapter exposes initial start, progress and stop; active takeover remains disabled. Central response
integration is limited to the guarded paths below; unbound legacy behavior
remains available. Bound worker stop and progressive remux remain disabled.
Production callback wiring, owner-supervisor activation, generation replacement,
selected-store progress fencing, and deployment/suspend acceptance remain
activation requirements.

## Isolated owner supervision

`RuntimeOwnerLeaseV3` supervises one captured attempt ID, row incarnation,
process-boot owner UUID, and epoch. `Postgres.RenewAttemptLease` renews that exact
fence and returns the database time sampled after acquiring the row lock. A
renewal that waits beyond the persisted lease expiry cannot revive the owner.
The database keeps an existing longer lease, including outstanding grant bounds,
and clamps extensions to attempt retention.

The supervisor uses the same explicit timing policy and suspend-inclusive clock
as runtime grants. Its local deadline is request-start elapsed time plus the
smaller of the returned database lease interval and configured maximum, less
the safety margin. This charges the entire database round trip and bounds local
authority even when the persisted lease is longer. Failed or late renewal,
expiry, cancellation, invalid identity, and clock failure permanently cancel
the supervisor. An independent watchdog checks expiry during a blocked renewal.
Reading a newer owner never changes the captured fence.

This foundation has no public lifecycle wiring. Its context and validity check
are inputs for future guarded preparation and publication; they do not write
progress, install a selected-store fence, replace an executor, or authorize
delivery. Those operations still require their own durable CAS or runtime grant.
Production activation must define the relationship between owner lease and
executor grant policies and satisfy the clock assumptions above.

Executor-bound node tracking uses keys qualified by logical session and the
complete executor namespace. Removal uses the retired runtime's captured
namespace; refresh and shutdown cleanup retain those qualified keys. Delayed
cleanup from an old process cannot remove or refresh a successor's record.
Legacy removal addresses only the legacy key. These Redis records are
diagnostic observations, not ownership proof: multiple generations may remain
visible until cleanup or TTL expiry.

## Central response integration

`GuardExecutorResponseV3` owns the response grant and writer for both worker and
central handlers. Callers must defer its cleanup and pass its returned request
and writer through all response work. Nil namespaces preserve legacy behavior;
callers must first compare actual runtime or metadata bindings so a missing
reference cannot select that fallback. The wrapper caps rolling deadlines and
has no `ReaderFrom` or `Unwrap` path around write checks. Provider errors close
any returned grant. Transport cancellation is exercised over HTTP/1 and HTTP/2.

Native direct-file and local encoded-HLS handlers require a signed executor
reference, authoritative recipe resolution, matching live metadata, and the
complete supported route assignment. A direct route uses execution `none` and
API egress; encoded HLS uses API execution and egress. Bound cold reconstruction
runs under the response context and the manager's execute-grant/resolver checks.
Error paths cannot call unfenced legacy progress or stop finalizers. Progressive
remux, video-copy HLS, and remote proxy chains remain refused until their complete
execution, serving, and finalization paths are guarded. Bound subtitle and font
requests are served only by the v2 sidecar producer, which runs the same signed
reference, recipe resolution, live metadata and serving-grant checks before
extraction; the legacy subtitle handlers keep refusing bound sessions.

Native signed references bind the recipe and metadata profile, while native
bearer delivery preserves the existing same-account authorization rule.
Compatibility delivery additionally requires the authenticated selected profile
to match. Owner supervision does not change either authorization rule.

Compatibility master, playlist, and segment handlers can serve an existing
local encoded-HLS runtime using the authenticated compatibility session's
stored reference and a matching authoritative recipe. They bypass legacy route
selection, startup, and recipe mutation. A stripped recipe reference is rejected
when local metadata or runtime still carries a binding. Cold compatibility
startup, progressive/remux delivery, copy-video HLS, and remote proxy chains
remain refused for bound sessions. Compatibility subtitle extraction is also
refused for bound metadata.

Ordinary local file reads can still block in the operating system. Response
cancellation and write deadlines prevent later authorized body writes; they do
not promise to interrupt every kernel filesystem operation. Deployment testing
must cover the storage and suspend behavior actually used.

A selected-store progress snapshot establishes a read source, not permission to
mutate progress. Before lifecycle activation, the selected sink must commit its
ownership fence, sequence watermark, progress/hints, and terminal receipt within
one transaction. A control-plane lookup followed by an independent selected-store
write is not atomic. Owner renewal and executor replacement must preserve this
boundary and the original logical playback identity. Dead-worker exit
acknowledgement cannot be an indefinite prerequisite for replacement.

The inactive [selected-store sink](playback-progress-sink.md) now supplies atomic
authority installation, sequenced progress and terminal receipts in PostgreSQL
and SQLite. Exact source handles bind the account, source UUID and selection
generation. Their local transactions do not activate the cross-store
coordinator or establish freshness after restore.

Legacy progress, stop, and finalization paths must refuse bound sessions before
calling personal-state writers. This includes HTTP and shared control helpers,
compatibility playback reports, and expiry/crash callbacks. Resource cleanup may
act on its exact executor object, but it cannot imply a durable stop receipt or
use a legacy progress/history writer. This restriction keeps delivery-only
integration from implicitly activating an unfenced lifecycle.

## Initial activation storage

`InitialActivationStoreV3` persists the initial control protocol. An explicitly
configured native playback handler exercises it for admitted sources; production
enablement and operational enrollment remain separate prerequisites. Source
registration is a separate account row. Its default admission state is blocked, and migrations
create no registrations. An explicit [PostgreSQL first-admission command](playback-first-admission.md)
can provision one existing account after enforcing the legacy transition barrier.
It exposes no source switch or cutover operation.

Before calling a selected source, the caller commits an immutable binding with
`BeginInitialActivation`: exact source reference, profile, logical session,
resolved progress target, attempt/incarnation/boot owner/epoch, `AdmissionID`
and `IntentID`. `AdmissionID` captures the registration's admission decision;
`IntentID` identifies this attempt's initial activation. Changing either token
does not refresh an existing binding. The caller resolves the progress target
before binding it; this storage layer does not infer a catalog target from a
file ID.

Every operation that reads registration and attempt authority locks registration
first, then the attempt, and samples database time after both locks. Source
calls and worker calls execute outside those transactions. Existing helpers
that need only the attempt lock never acquire registration afterward.

The forward phases are pending, installed and activated. Installation
acknowledgment requires a receipt with the exact initial fence and no progress
or terminal state. Acknowledgment and publication require the captured source
and admission token to be currently admitting, the exact activation intent and
owner to remain current, and both lease and retention to remain live. An
unchanged source reference alone is insufficient after admission is withdrawn.
Publication freezes the response and transitions the same attempt to active.
An exact retry compares the persisted decision; it does not allocate work.
Bound route staging and execution grants require acknowledged installation;
serve grants additionally require active control state. Begin refuses to adopt
an unbound attempt that has already issued any execution grant, including an
expired grant. This preserves installation-before-execution across every caller.

`InitialActivationReceiptV3` is an opaque, immutable observation read from a
trusted exact source handle. Its factory verifies source/account, scope and
the complete fence, then freezes the returned state. The read happens before
control storage is called. A bare state or zero receipt cannot acknowledge
installation or abort. A receipt establishes a past observation; control
storage still checks current admission, phase and authority when accepting it.

`ReadInitialActivation` can inspect an exact retained binding after lease or
retention expiry. Reading an activated phase resolves a previously committed
outcome only: it neither renews rights nor authorizes publication, execution or
automatic allocation. Publication retries after expiry fail even if a prior
publication committed.

Initial abort covers pending and installed bindings. It is eligible when the
initial owner's lease expired or registration no longer admits the captured
decision. It records one immutable abort ID and moves control state to
draining, closing grant issuance. Its persisted drain deadline is at least the
maximum issued grant deadline, evaluated using database time. Retries preserve
that deadline rather than extending it.

The initial caller resolves an uncertain install against the exact source and
can cancel its own pending or installed intent with `CancelInitialActivation`.
Cancellation uses the same stable abort ID and grant drain as expiry abort.
Reconciliation installs the exact captured fence, stops it and reads its
committed terminal receipt. A matching
already-stopped fence is valid evidence even when its stop ID differs from the
abort ID. Completion requires that exact receipt and the elapsed grant drain;
it persists the first terminal receipt and transitions to aborted/stopped.
Missing source files and failed source reads preserve the unresolved intent.
An activated binding cannot enter this initial abort protocol.

Bound attempts cannot use legacy publication, stop or drain completion to skip
the source receipt. They cannot use expired preparing reclamation or the
legacy save path's expired-row deletion to erase the binding. Cleanup retains
pending, installed, aborting and activated bindings regardless of retention
expiry. Only a completed initial abort or normal stop with its terminal receipt and
elapsed drain can become eligible for ordinary retention cleanup. Source receipts
remain in the selected database, so a delayed original install replays stopped
state after reconciliation.

## Opt-in initial handler and normal stop

`ConfigureInitialPlaybackV3` requires a boot owner, the control store, exact
source provider, immutable recipes, runtime grant clock/policy and grant/recipe
callbacks. No application startup wiring enables it by default. Ordinary starts
read an explicitly admitted registration; they never provision or admit a source.

The handler reserves a captured session ID in the session manager without making
it visible. It installs and acknowledges the exact source fence before staging
an executable route. The immutable recipe is published before execution; durable
activation precedes local session visibility. Lost install or publication replies
are read back from their authority. Uncertain publication retains the same staged
snapshot and owner, so a retry can publish that snapshot after confirming active
control state. It cannot create a replacement executor. Owner loss discards an
unpublished local reservation without pretending the sink has stopped.

The binding freezes progress duration, persistence policy, thresholds, version
hints and history identity. Progress replaces only the client's sequenced sample;
retries cannot re-resolve policy and change an otherwise identical payload.
`BeginBoundStop` closes grant issuance and records one stop ID and drain deadline.
The exact source commits the optional final sample and terminal receipt before
`CompleteBoundStop` marks control stopped. Normal completion requires that same
stop ID, unlike initial abort's already-terminal reconciliation. A draining
response is pending; only the completed receipt permits local session removal.
Repeated stop requests use retained authority after lease expiry and grant no
new execution or progress rights. See [the wire contract](../playback-api.md).

PostgreSQL first admission is an explicit operation, separate from runtime
enablement. SQLite provisioning, retirement/cutover, restore, takeover and replacement
remain inactive. Every configured runtime runs bounded reconciliation across accounts. It retries initial aborts and completes normal stops with an
existing matching source receipt; a stop without that receipt still requires the
original client body. Each scan advances past visited rows even if a source exhausts the page deadline.
The runner joins application shutdown. The initial protocol does not supply the later retirement-before-seal workflow.
