# Native realtime work admission

The native events and Watch Together sockets share one router-owned admission
controller across v1 and v2. It bounds charged client attempts before snapshot
reads, playback/session reads, room locks and transactions. Existing authority,
byte limits, ordered output queues and disconnect cleanup still apply.

## Units and limits

| Work class | Member second/minute | Account second/minute |
| --- | --- | --- |
| Event selection/snapshot batch | — | 20 / 120 |
| Room recovery/report | 8 / 360 | 32 / 1,440 |
| Explicit room action | 4 / 30 | 8 / 120 |

Both aligned fixed windows must pass. These are permit counts, not token buckets
or sliding-window guarantees. An event unit covers the complete accepted batch,
including unchanged selections, empty selections and in-memory/null snapshots.
The current channel allowlist has eleven entries. A room unit covers one attempted
operation, including ownership lookups and the room transaction. Errors after
admission spend the unit.

`Connect`, `attach_session`, `state_report`, `ready` and `buffering` use recovery
capacity. `transport_request` and v2 `lobby_ready` use action capacity. Each report
and ready message is charged separately. A host report can itself persist an
anchor and issue commands, so the action gate does not bound every room command.
JSON `ping`, malformed/unknown messages and v1's unsupported `lobby_ready` spend
only local input capacity. Protocol control frames, authority checks, disconnects,
retirement, timers and internal reconciliation do not spend client work capacity.
Client message types cannot select those internal paths.

The member identity is `(users.id, profile_id, room_id)`; account identity is
`users.id`. Profiles, credentials, login sessions, reconnects and API versions
share the account counters. Member admission precedes account admission. A denied
member need not inspect the account. A later denial or cancellation can spend
earlier credit; no refund or cross-key transaction is promised. There are at most
two store decisions per room attempt and one per event batch, under one total
250 ms deadline bounded by caller cancellation. Handlers check cancellation before
entering downstream work. Already entered work keeps its existing cancellation
semantics. The server never queues work waiting for replenishment.

A full second window on either side of a boundary can be admitted in a short
interval. An interval of length T intersects at most `ceil(T/W)+1` windows of
width W. Multiply that by the window limit for an upper bound; it is not an
exact sliding-window rate. Per-account caps yield an account-proportional room
bound. They provide no fixed room ceiling, household-profile reservation,
recipient-liveness guarantee, maximum lock wait or fairness scheduler. An
admitted operation can still fan out to every room member, with remote
reconciliation as a separate cost. Connection cardinality, server-originated
snapshots and independent HTTP mutations are outside this bound.

## Backend lifetime and failure

The active startup `ratelimit.backend` selects a dedicated work store even when
HTTP limiting is disabled. HTTP rates, counters, enablement and reloads do not
clear or configure this store. Memory is the default and enforces per node:
N nodes can grant N times the account allowance. Its aligned time is one startup
wall-clock anchor plus monotonic elapsed time. Redis uses store `TIME` within an
atomic script and a stable, version-neutral key namespace. The dedicated Redis
client honors context deadlines. A missing configured Redis connection prevents
startup; an unavailable Redis store refuses new work without local fallback.

Second counters expire two seconds after their first increment; minute counters
expire after 120 seconds. Denial does not extend retention. Redis retains live
window counts when store time revisits them; a bounded retained-window map
refuses work if an extreme time regression fills it. Expired windows or lost
acknowledged Redis writes can reset capacity. The shared guarantee therefore
assumes retained store state and no return to expired windows; it does not prove
wall-clock bounds or failover linearizability.

V2 capability responses, both v2 ticket responses and the events v2 `hello`
include `work_admission`: `version: 1`, `backend`, `scope` (`per_node` or `shared`),
`epoch`, the limits, fixed-window semantics and admission deadline. The epoch
identifies the local controller lifetime; Redis counters may survive that epoch.
Startup logs report backend, scope and epoch. There is no room hello or new v1
success frame. Capability state describes support, not current Redis health.

## Initial work and input shielding

V2 ticket mint validates existing authority/proof before charging initial work,
then stores one prepaid permit in the existing single-use server-side ticket.
No ticket is issued on work refusal. Failed or unused tickets burn credit.
The original deadline is at most 30 seconds and is never extended by storage or
consumption. An events URL declaration consumes that credit before upgrade;
without a declaration the first valid subscribe uses it. Later subscribe frames
are charged, including a duplicate startup selection. Expired or legacy missing
credit requires ordinary admission. Room Connect consumes its prepaid admission
before its downstream reads/upgrade; a legacy ticket without the flag pays at
that boundary. No credit survives replacement.

Initial work can start from permits minted during the preceding 30 seconds.
The window-intersection bound for initial starts therefore includes that prior
interval, plus clock skew between minting and consuming nodes. It is not a
same-window ceiling on starts. V1 has no prepaid ticket: URL selections and
Connect are charged before upgrade. A failed upgrade never refunds them.

After a bounded application-message read, local token buckets allow 16 messages/s
with burst 32 per socket, then 64/s with burst 128 per account on the node. A
refused socket bucket never touches the account bucket. Account credit persists
across reconnects and is evicted after ten idle minutes. These shields bound
parsing and shared-store attempts; they do not impose a cluster ban or connection
quota. Exceeding either shield sends at most one `realtime_input_limited` error,
then closes only that socket with 1008 and reason `realtime input shield` through
its ordered writer. Work refusal keeps healthy sockets open.

## Refusal and recovery contract

Work refusal is an `error` with `realtime_rate_limited`, a human `message`, optional
echoed `request_id`, `retry_after_ms`, and `work_started: false`. No downstream
work for that attempt started. Retry is the next boundary of every exhausted gate
checked, rounded up, with a 250 ms minimum and 60,000 ms maximum. It is a lower
bound, not a reservation. Store errors/timeouts use `realtime_unavailable` and
2,000 ms. Ticket refusal uses HTTP 429/503, `Retry-After` and the same structured
metadata in the v2 Problem Details envelope. V1 pre-upgrade refusal carries JSON
metadata and `Retry-After`; browser WebSocket APIs may hide that handshake body.

On an events refusal, the accepted selection stays active: no subscribed
acknowledgement or snapshot is sent for the refused batch. Successful admission
preserves acknowledgement, full ordered snapshots, selection publication and
live dispatch ordering. Initial-subscribe refusal renews the missing-subscribe
grace to 65 seconds from that refusal; authority loss and absolute expiry win.
No snapshot coalescing or server pending-work queue is introduced.

V2 one-shot `transport_request` and `lobby_ready` success adds
`{"type":"action_result","request_id":"…","action":"transport_request","status":"applied"}`
after existing operation snapshots/commands and successful service completion,
including no-ops. Refusal has no success acknowledgement. Downstream errors echo
the request ID with `work_started: true`; they never authorize automatic replay.
IDs correlate attempts and are not deduplication receipts. V1 success shapes
remain unchanged and use authoritative snapshot echoes with weaker correlation.

Coordinated clients keep one pending timer, honor the advertised minimum and add
0–250 ms nonnegative jitter. Unavailability backs off 2, 4, 8, 16, 30 seconds;
opaque handshake failures use 1, 2, 4, 8, 16, 30 seconds. The input-shield close
uses 5, 10, 20, 30 seconds and is recognized by its reason, not every 1008.
A mere socket open does not reset backoff; shield recovery needs 30 seconds of
useful accepted operation. Existing streams and clock pings continue through
work refusal. Store outage can stall attachment, readiness and controls
indefinitely; an unready waiting room does not resume merely because its
ten-second deadline passed. Valid previously prepaid work may still finish.

Events keep the latest desired complete selection, cancel replaced timers and
correlate acknowledgements. Web batches mount/unmount changes per microtask;
explicit later refreshes remain real batches. Android avoids URL-plus-frame
duplicate startup. URL-only observers recover through fresh-ticket/handshake
backoff. One-shot transport intent is automatically retried only after an
explicit correlated pre-work refusal, once within five seconds, with unchanged
room, selection revision, generation, connection epoch, playback session,
request ID and authority. Lost acknowledgement, timeout, cancellation, socket or
authority change and downstream errors reconcile and show uncertainty without
replaying. Seek input retains the latest absolute target per 250 ms window;
other distinct controls are not merged.

Desired attachment, readiness, buffering and lobby readiness keep at most one
retry per action kind per engagement. Recompute at execution and cancel when
desired state, command/session/selection/authority or engagement changes.
Snapshot echoes, rather than socket-send success, establish saved state.
Periodic reports obey refusal floors without mixing identities. Buffering heals
a refused attempt and never retries a recovered stall; lobby retries expire on
leaving the lobby. Apple has no generic events consumer; its playback-control
socket and Jellyfin KeepAlive/UserDataChanged are outside this change.

## Compatibility and verification

This critical-security bridge exception explicitly permits v1 work refusal,
retry timing/optional metadata and input-shield closure. It does not add v1
success messages. Older v2 clients can lose a one-shot action on unfamiliar
refusal; compatibility is not transparent. Server rollout must wait for named
compatible web, Apple and Android release baselines and their recovery evidence;
third-party v1 release notes must describe refusal and uncertain delivery.

Finite fake-clock/store and direct-call regression tests verify admission
arithmetic, cancellation, handler placement and ordered synthetic service output.
They do not measure capacity, Redis failover, room fairness, queue drainage or
real-client playback. Environment-gated Lua integration tests require separate
execution; adapter tests alone do not prove script atomicity or TTL behavior.
