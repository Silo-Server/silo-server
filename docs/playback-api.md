# Initial playback API

The default v2 runtime advertises `sequenced_progress_v1` in successful start
responses. The decision protocol remains version 3. Authenticated capability
discovery or first start automatically establishes a missing PostgreSQL source
through retained first admission; no account enrollment command is required.
Existing authority conflicts, blocked registrations and retiring sources refuse
admission rather than creating replacement authority.

The flow supports original-file delivery and encoded or remuxed video HLS through
API or proxy egress, including selected remote execution. Progressive remux has no
bound producer and is excluded during planning. A client that offers progressive
and HLS may therefore receive HLS with audio conversion. If no executable route
remains, the server returns a terminal adaptation refusal. Bound hardware execution
selects a concrete device before freezing the recipe and requires unchanged
execution policy between preparation and start.

The typed v2 adapter calls the shared playback application service. Its routes
are always registered and require an authenticated profile. The capability
reports `state` (`not_configured`, `not_admitted`, or `available`), `allowed`,
a revision, supported deliveries and features. When configured, `installation_id`
is the persisted server instance UUID also used by diagnostics; clients must
capture it and include it in each mutation. A mismatch returns 409
`installation_changed`. Validation failures use 422; unavailable dependencies
use 503. The decision protocol remains version 3.

| Operation | Method and path | Success |
| --- | --- | --- |
| Capability | GET `/api/v2/playback/capabilities` | 200 capability state |
| Start | POST `/api/v2/playback/start` | 201 decision |
| Progress | POST `/api/v2/playback/{session_id}/progress` | 200 accepted state |
| Stop | DELETE `/api/v2/playback/{session_id}` | 202 draining or 200 receipt |

Start uses the version 3 decision request plus `installation_id`. File IDs in
v2 requests and decisions are opaque JSON strings; track indices remain numbers.
Allocate one attempt ID and retain the exact request for uncertain retries.
Returned media URLs are opaque. The v2 adapter projects local direct and HLS
paths into the v2 namespace without changing the persisted decision or its signed
query. Replaying the same decision through v1 retains the original v1 URL.
The capability does not advertise replan or route-event support.

The initial flow serves these raw media operations:

| Operation | Method and path | Success |
| --- | --- | --- |
| Original bytes | GET `/api/v2/stream/{session_id}` | 200 or 206 |
| Original metadata | HEAD `/api/v2/stream/{session_id}` | 200 or 206, no body |
| HLS manifest | GET `/api/v2/playback/transcode/{session_id}/master.m3u8` | 200 |
| HLS segment | GET `/api/v2/playback/transcode/{session_id}/segment/{name}` | 200 or 206 |
| Subtitle sidecar | GET `/api/v2/stream/{session_id}/subtitles/{track}` | 200 |
| Subtitle metadata | HEAD `/api/v2/stream/{session_id}/subtitles/{track}` | 200, no body |
| Subtitle fonts | GET `/api/v2/stream/{session_id}/subtitles/{track}/fonts` | 200 JSON array |

Delivery requires account authentication, viewer authorization and the opaque
signed `st` executor reference. Media elements may carry account authentication
in the existing `token` query parameter. Explicit profile selection and PIN proof
use the existing viewer headers. The underlying transport checks the exact
session, executor namespace and live source/owner grants before serving bytes.
An unconfigured initial flow, unbound legacy token or expired authority fails
closed. HLS segment references remain relative to the v2 manifest path.

Subtitle sidecar URLs and font-bundle URLs in the decision's `subtitle.inventory`
and `subtitle.artifact` are v2, API-local and carry the same signed `st` reference
as the media bytes; a track without a sidecar shape (burn-in only) has no URL. The
sidecar producer admits a request exactly as the media bytes are admitted, then
resolves the inventory ordinal against the plan's effective or requested file
(`file_id`), the stable downloaded-subtitle identity when present, and streams the
extracted text, ASS or `.sup` bytes under the held serving grant. The font bundle
is a typed JSON array of `{name, data}` (base64) for embedded ASS/SSA tracks. The
legacy `/api/v1/stream/{session_id}/subtitles/...` handlers keep refusing bound
sessions. A missing source file is a 404 and never runs the legacy session abort;
terminal state stays with the sequenced stop and reconciliation paths.

Original files and segments retain byte ranges and conditional requests; HEAD
retains original-file metadata without a body. Unsatisfiable ranges return 416
`range_not_satisfiable` with `Content-Range` when available. Errors before success
bytes are safe v2 problems; a later delivery failure terminates the stream.
Native media bypasses response compression and JSON buffering. These routes do
not expand the advertised initial flow to legacy sessions, remux or remote execution.

The existing v1 routes use the same mutation service for bound sessions:

| Operation | Method and path | Success |
| --- | --- | --- |
| Progress | POST `/api/v1/playback/{session_id}/progress` | 200 accepted state |
| Stop | DELETE `/api/v1/playback/{session_id}` | 202 draining, then 200 terminal receipt |

Progress accepts `{"sequence":42,"position":120,"is_paused":false}`; v2 also requires `installation_id`. Sequence
is a positive signed 64-bit integer scoped to the captured playback session.
Allocate it once per sample and preserve the entire body on retry. A higher
sequence supersedes an earlier sample even when its position moves backward.
An older sequence returns the latest accepted state. An equal sequence with a
changed payload returns 409 `progress_conflict`.

The response has `outcome` (`applied`, `replayed` or `stale_sample`) and optional
`accepted`, containing `sequence`, `position` and `is_paused`. Accepted position
is the raw winning sample, not a maximum position or resume-policy projection.

Stop accepts `{"stop_id":"<canonical UUID>"}`; v2 also requires `installation_id`. It may also include `sequence`,
`position` and `is_paused` as one final sample; sequence and position must appear
together. Omitting both uses the last accepted sample. Generate the stop UUID
once and preserve the exact body through lost replies and draining retries.
Drain retries must not allocate another session or infer completion from a timeout.

A 202 response has `outcome="draining"`. Retry the same DELETE and body with
bounded backoff until 200, surfacing a pending or failed stop when the retry
budget expires. The completed response has `outcome="stopped"` or `"replayed"`,
the stable `stop_id`, optional `accepted`, and `history_id` when history was
created. No history is fabricated when persistence is disabled or no sample
qualifies. A client can persist the pending request to retry after restart;
that request is not authority and remains subject to server checks.

Unavailable authority remains uncertain unless the server returns the exact
retained owner-loss recovery described below. Authentication, installation and
profile checks still apply. The selected source, fence and frozen progress policy
are server-owned and cannot be supplied by a client.

### Initial startup-abort recovery (v2)

An exact original START can resolve a server-canceled initial activation after
an API restart. The authenticated account, profile, installation, original
request and device identity must still match. Recovery uses the captured source
and fence; it never restores a source, renews a lease, or grants execution.

After the durable abort completes its grant drain and retains the actual source
terminal receipt, an abort with no accepted progress or history returns the
existing 201 `adaptation_unavailable` decision with
`terminal.reason: "playback_start_aborted"` and `terminal.retryable: false`.
It contains no session, plan, recovery envelope or client STOP acknowledgement.
Only a new explicit Play may create a fresh attempt after that terminal decision.

An incomplete abort may close its already-installed exact source fence using its
original server abort ID, without a final sample. Missing or changed source
state, unfinished drain, or a receipt requiring accepted-progress semantics
remains unresolved. The original START must be retained; generic 404/503 responses
never prove termination. This ordinary cancellation is not `owner_lost` and does
not change the owner-loss START/STOP protocol below.

### Lost API-owner recovery (v2)

An exact original START or STOP can terminally recover an activation-backed
attempt after its API owner's lease or retention expires. Recovery never takes
over the owner or grants a new route. It closes the captured source fence without
adding a final sample, then waits for the database-time maximum of current,
candidate, output, auxiliary and retained replacement grant deadlines.

Every recovery envelope requires `recovery_id` (the persisted abort UUID),
`playback_attempt_id`, `session_id`, `state` (`draining` or `aborted`) and
`reason: "owner_lost"` inside `recovery`. The nested session ID identifies the
old activation, including when the original START reply was lost; it is not a
playable session receipt.

| Exact request | Pending | Completed |
| --- | --- | --- |
| Original START | 202, `outcome: "draining"`, recovery only | 201, protocol 3 `adaptation_unavailable`, `terminal.reason: "playback_owner_lost"`, `terminal.retryable: false`, aborted recovery |
| Original STOP | 202, `outcome: "draining"`, recovery only | 200, `outcome: "aborted"`, aborted recovery |

Completed recovery includes `recovery.accepted` only when the captured terminal
source receipt has a Last sample. It contains the stored `sequence`, `position`
and `is_paused`; bound timelines retain `timeline_id`, local `position` and global
`item_position`. Pending recovery has no accepted sample. The client's uncommitted
STOP final sample is never applied or acknowledged by owner-loss recovery.
Neither recovery response contains a top-level plan, session, stop ID or history ID.

A previously persisted ordinary STOP always retains its original StopID and
ordinary response semantics. Once an owner-loss `202` has been emitted, its
persisted abort identity prevents a later ordinary StopID receipt for that retained
attempt: replies stay in the recovery union or remain uncertain errors. An ordinary
STOP can win only before recovery is committed. If its source receipt is missing, only the exact
original client payload can complete it. Recovery cannot substitute an abort.
Exact START replay checks the original body/device digest before recovery and
never rewrites the historical START decision. Terminal tombstones remain available
for the normal retention period after completion.

Clients must validate the original installation/account/profile/session/attempt
binding before releasing a recovery hold. Legacy journals lacking an original
attempt ID remain fail-closed for owner-loss recovery; ordinary STOP receipts still
work. There is no first-observation binding or reconstruction from an ambient plan.
Only an explicit new intent after terminal settlement may allocate a new attempt.
Fresh admission for the same source/profile is blocked by unresolved lost-owner
work; unrelated live attempts remain legal. Source withdrawal or reselection,
activation-less reservations, generic errors and timeouts remain uncertain. No
new feature token or automatic fresh-request retry is introduced.

Web consumption is feature gated. Apple and Android adoption is coordinated
separately and must be verified before production enablement. Jellyfin reports
cannot mutate a bound session through legacy writers; Jellyfin lifecycle
adoption remains outside this opt-in native flow.

The web client persists the exact start request before dispatch, then persists
sequence allocation, pending progress and stop bodies in browser storage.
Web Locks serialize mutations across tabs. Records bind the installation,
account, profile, origin and session; credentials are not stored. Identity changes
quarantine old requests. Reload automatically replays an uncertain START with its
original bytes. If it recovers a session that never reached the player, it stops
that exact session through the durable STOP path. A new Play resolves the retained
attempt before dispatching its new request. Transport failures, server errors and
draining receipts retry within a 60-second budget; recovery never mints an attempt
or falls back to legacy playback. Video and audiobook hosts share the recovery
task and display one Retry notification only if automatic recovery cannot finish.
A validation rejection, including `422 validation_failed`, retains its journal:
it cannot prove that an earlier dispatch never allocated a session. The current
API has no authoritative no-allocation receipt. Storage
or Web Locks unavailability fails configured playback before dispatch. Clearing
or evicting browser storage loses recovery state; unload cannot guarantee polling.

Normal application startup wires `NewInitialPlaybackRuntime` through the router's
`InitialPlayback` dependency. The constructor verifies the persisted installation
identity and requires source, owner and media-grant policies. Ordinary starts may
perform first admission through the retained PostgreSQL admission protocol; they
never bypass source markers or authority checks.
`internal/playback/testfixture.ProvisionPostgres` enrolls a newly created synthetic
account transactionally; it cannot enroll an existing account.

Every configured playback runtime runs bounded reconciliation across accounts;
no account list or operator opt-in is required. Database compare-and-set transitions
and exact source receipts allow multiple API replicas to complete the same work
safely. The scan advances past visited rows even when a slow source exhausts a
page deadline, and the runner joins application shutdown.
A failed START performs its own durable abort and waits briefly for the source
receipt and database drain deadline. Once both complete, it returns the existing
`201 adaptation_unavailable` decision with terminal reason `playback_start_aborted`.
If completion is still uncertain, retained state remains available for automatic
reconciliation and exact START replay. Remote transcode startup uses the same
30-second manifest readiness budget as local startup; a running process alone
does not count as ready.

Expired activated attempts move through owner-loss terminal recovery.
Expired or withdrawn pending/installed starts move through durable abort intents;
aborting intents retry their exact source operation. A stopping intent completes
only after its matching source terminal receipt and drain deadline exist, then
closes matching local runtime state. If the source stop has not committed, the
original client request must retry: reconciliation does not invent or omit a final
sample. The runner does not adopt active owners or implement takeover, restore,
replacement, cutover or general production source provisioning.

## Route events (v2)

`POST /api/v2/playback/route-events` (`reportPlaybackRouteEvent`, `non_retryable`) records one
diagnostic event for an attempt this profile owns. The body is the v3 event plus
`installation_id` and a client-minted `event_id`; `202` returns that id with
`outcome: accepted`, which acknowledges queueing, not a durable write. A retry with the same
`event_id` is recorded once (partial unique index on attempt and event id); clients still
never retry automatically and treat `429` as drop. Diagnostics are reduced to the approved
key set before recording. Legacy v1 reports carry no id and keep their unconditional insert.

The web player reports through v2 only for sessions started by the durable v2 flow, using the
captured installation, account, profile and origin; a legacy session keeps its v1 call.
`playback_route_diagnostics` stays absent from the initial feature set.

## Replan (v2)

`POST /api/v2/playback/{session_id}/replan` (`replanPlayback`, `domain_identity`) replans an
admitted initial attempt under its live owner lease. The body is the v3 replan request plus
the `installation_id` the attempt was started with. The initial flow serves exactly one
intent: a position re-anchor of the current direct route (`seek_reanchor`,
`seek_failure_recovery`, or a `failure_recovery` whose quality, tracks and output are
unchanged). The plan keeps its identity, its timeline moves to the requested position, and
the stream token is re-signed for the same executor-bound session. Encoded HLS attempts,
and any track, quality, output or client-feature change, answer `501 capability_unsupported`
with nothing written; the client starts a new attempt at the target position. A
`replan_request_id` replays its committed decision on retry and answers `409` when reused
with different input, when the named plan is no longer current, or when a newer replan is
already active; `404` when the session is not this profile's; `503` while the owner lease
cannot be confirmed. The legacy replan writer still refuses authority-owned rows: the bound
writer admits only the captured owner, epoch and incarnation while the lease is live, and its
commit is a compare-and-swap on the base revision that never moves the retention deadline or
the staged executor route.

The web player replans through v2 only for sessions started by the durable v2 flow, using the
captured installation, account, profile and origin; a legacy session keeps its v1 call.
`seek_reanchor_v1` and `output_change_v1` stay absent from the initial feature set because
the flow does not implement them in full.

## Run the synthetic router fixture

Commands assume the repository root is the current directory. Use Go from
`go.mod`, FFmpeg with `libx264` and AAC encoders, PostgreSQL, and Redis. Set
`SILO_PLAYBACK_ROUTER_TEST_DATABASE_URL` to a fresh disposable database owned by
this fixture, and `SILO_TEST_REDIS_URL` to a disposable Redis instance. Do not
reuse a deployment database: this fixture initializes server settings using its
own test cipher and creates synthetic catalog and login-session data.

```sh
: "${SILO_PLAYBACK_ROUTER_TEST_DATABASE_URL:?Set a fresh disposable PostgreSQL URL}"
: "${SILO_TEST_REDIS_URL:?Set a disposable Redis URL}"
playback_migration_key=$(openssl rand -base64 48)
DATABASE_URL="$SILO_PLAYBACK_ROUTER_TEST_DATABASE_URL" \
  SECRET_KEY="$playback_migration_key" \
  go run ./cmd/silo --env '' --migrate-only
unset playback_migration_key

go test -race ./internal/api \
  -run '^TestInitialPlaybackRootRouterV2Synthetic$' -count=1 -v
```

The migration command only migrates and exits. The test opens an ephemeral HTTP
listener through `NewRouter`, generates two seconds of synthetic H.264/AAC media,
creates a fresh admitted account/profile and authenticated login session, then
checks capability, start, returned media bytes, progress and stop. It also checks
that an unregistered account cannot acquire source admission through a start.
The test closes the listener and removes its synthetic account/media on exit;
it does not leave a browser- or native-client-accessible server running. Discard
the dedicated database and Redis instance after the test.

For control-store, reconciliation and local-HLS checks, set
`SILO_TEST_DATABASE_URL` to a second fresh disposable database. Apply migrations
with the same migration-only command, substituting that variable for the router
fixture database, then run:

```sh
: "${SILO_TEST_DATABASE_URL:?Set a separate migrated disposable PostgreSQL URL}"
go test -race ./internal/api/handlers \
  -run '^TestInitialPlayback(Reconcile|HTTPLocalTranscode|TerminalCleanup|Capabilities)' \
  -count=1 -v
go test -race ./internal/playback/planstore ./internal/playback/testfixture \
  -run 'InitialReconciliation|ProvisionPostgres' -count=1 -v
```

## Run a persistent disposable harness

`cmd/playback-synthetic` is a separate executable. It requires explicit
`--synthetic-only` acknowledgment and refuses databases containing users or media.
It does not change `cmd/silo` or ordinary startup. Provide a fresh migrated
disposable database, a disposable Redis instance, and a stable encryption key:

```sh
: "${SILO_SYNTHETIC_DATABASE_URL:?Set a fresh disposable PostgreSQL URL}"
: "${SILO_SYNTHETIC_REDIS_URL:?Set a disposable Redis URL}"
: "${SECRET_KEY:?Set a stable disposable database encryption key of at least 32 characters}"
DATABASE_URL="$SILO_SYNTHETIC_DATABASE_URL" \
  go run ./cmd/silo --env '' --migrate-only
playback_harness_dir=$(mktemp -d)
go build -o "$playback_harness_dir/playback-synthetic" ./cmd/playback-synthetic
"$playback_harness_dir/playback-synthetic" --synthetic-only
```

The listener defaults to an ephemeral loopback port. The harness prints the path
to a private bootstrap JSON file containing its URL, synthetic username/password,
profile ID, file/content IDs, installation ID and media path. Read that file
locally; it contains credentials. No deployment credentials or database URLs are
written into it. A fresh synthetic account is created on each invocation, then
only that account receives a generated password for normal password login.
The harness never adopts or changes an existing account.

Use the returned URL and credentials in a client that supports the negotiated
initial flow. For the web development server, set `VITE_API_PROXY_TARGET` in
`web/.env.local` to that URL before `make dev-frontend`. The harness serves the
API; the Vite server supplies the development UI. For physical-device testing,
`--listen` can bind an explicitly chosen reachable interface and port. Use that
address in the client; ordinary loopback URLs are reachable only from the host.
The harness uses HTTP and provides no external deployment or TLS setup.

The default lifetime is one hour; `--duration` selects a shorter bounded run.
`--bootstrap` selects the output file location. SIGINT/SIGTERM or duration expiry
stops the listener, cancels the runtime, removes owned fixture rows and deletes
temporary media/bootstrap data. Forced termination cannot run cleanup; discard
the disposable database and Redis resources after such a run. Never clear or
substitute real deployment data to satisfy the empty-database check. Reusing the
cleaned disposable database requires the same `SECRET_KEY`, since encrypted
server settings and the installation UUID remain persisted.

Run the smoke check in another terminal with the printed bootstrap file path:

```sh
: "${playback_bootstrap_file:?Set the private bootstrap file path printed by the harness}"
python3 scripts/playback-synthetic-smoke.py "$playback_bootstrap_file"
```

It uses normal v2 password login, checks the configured capability and persisted
installation UUID, starts direct playback, replays the same attempt, compares
media bytes, reports progress, rejects a different installation identity, then
retries the exact stop body until a terminal receipt and verifies stop replay.
It does not print credentials. Keep the harness running while testing.

The persistent harness disables transcoding and advertises original-file delivery
only, so shutdown cannot leave an FFmpeg playback worker running. Local-HLS
coverage remains in the separate handler integration tests above.

The harness calls `testfixture.ProvisionPostgres`,
`NewInitialPlaybackRuntime` and `NewRouter`, with bounded reconciliation scoped
to its new synthetic account. The tested owner policy is 30 seconds with a
1-second safety margin and renewal 10 seconds before expiry; media grants last
1 second with a 100-millisecond margin and renewal 200 milliseconds before
expiry. Both poll every 10 milliseconds. These are test settings, not production
tuning. Generated media is synthetic H.264/AAC; broader media compatibility and
physical-device behavior require their own tests.

Native consumer adoption and manual device validation remain coordinated work.
Release acceptance also requires a named owner to ratify the redesigned start
row in the migration ledger; until then
`TestDeclaredRetrySafetyMatchesTheLedger` rejects the unmapped `startPlayback`
operation. The harness does not enable normal startup, real-user enrollment,
takeover, restore, replacement, remux or proxy delivery.

### Initial playback through proxy auxiliary routes

With initial playback enabled, the restart-required
`SILO_INITIAL_PLAYBACK_API_ORIGIN` setting joins API subtitle/font production to
selected proxy egress. Initial and successor plans publish credential-free
`/stream/v3/{session_id}/subtitles/{track}` and font URLs at that proxy's origin,
retaining the source-file and subtitle identity query selectors. Clients must
validate the returned origin, path and `X-Profile-Id` against the original start
request before supplying that request's captured in-memory bearer to subtitle and
font fetches. The bearer is never part of the plan or immutable recipe. See
[auxiliary transfer authority](architecture/playback-auxiliary-transfer.md).
