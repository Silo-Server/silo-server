# Client event socket admission

One `events.ClientSocketAdmission` is constructed at startup, registered as a
Prometheus collector, and passed independently to both native handler instances
and Jellyfin. It does not depend on the optional event hub. A nil or zero
admission refuses; routers must never construct a second allowance.

The production constructor uses observation mode. Thresholds are 64 pending or
active sockets per account per API process and 2,048 per process. The startup-only
`SILO_EVENTS_MAX_SOCKETS_PER_ACCOUNT` and `SILO_EVENTS_MAX_SOCKETS_PER_PROCESS`
values accept positive base-10 Go integers after trimming whitespace. Empty uses
the default; invalid, overflowing, zero or negative values warn using only the
variable name and safe fallback. Effective thresholds and observation mode are
logged once. A process threshold below the account threshold is valid. No
runtime toggle enables enforcement.

## Invariants

A mutex protects exact process and account counters. Native `claims.UserID` and
compatibility `Session.StreamAppUserID` identify the login account; profiles,
credentials, devices, API versions and normalized aliases share the allowance.
Every physical client connection reserves once before upgrade, subscriber
allocation or recurring authority work. Internal `Hub.Subscribe` consumers,
including the plugin dispatcher, do not reserve client capacity.

V1 checks claims and origin, reserves, then checks optional profile binding and
acting authority. V2 checks shape/origin, consumes its one-use ticket, validates
identity, then reserves. Nil/draining may reject before ticket consumption; a
quota-denied ticket stays spent. Minting a ticket is not a reservation. Jellyfin
validates the session/key, reserves, upgrades, then starts optional user-data
forwarding. KeepAlive-only connections follow the same accounting.

Failed upgrades and canceled attempts release through the same idempotent
reservation. On teardown, close the transport, stop its reader and unsubscribe
before release. The application context cancels admitted connections even though
HTTP shutdown does not drain hijacked transports. Draining is the shutdown
predicate or canceled application context and refuses in both modes. One startup
shutdown-work registration waits for admission drain and zero reservations; it
is not registered per socket and does not claim every unrelated goroutine joined.

Enforcing mode is available to finite tests: account refusal is `429`, process or
draining is `503`, with `Retry-After: 5` and `Cache-Control: no-store`. Process
capacity wins when both limits are reached. Observation records exceeded account
and process dimensions independently and admits both. Draining is a real refusal.
No eviction, identity replacement, renewal priority or unlimited sentinel exists.

There is no Redis admission state, lease or cluster quota. N API processes can
grant N times the account allowance. Process death removes its counters;
replacement processes start with their own live accounting, without an expiry
timer. A partitioned old process can continue alongside a replacement. Admission
has no store-outage mode; existing auth, tickets and event delivery retain their
own store dependencies and failure semantics.

## Observation and later enforcement

Observation is not remediation and bounds no socket resources. Defaults are
unmeasured starting thresholds. No pilot, host selection, deployment or
enforcement is authorized by this implementation.

The collector exports process `occupancy` and `high_water`, an unlabeled
`account_occupancy` admission histogram with buckets 1, 2, 4, 8, 16, 32, 64, 128
and overflow, and `max_account_high_water`. Names share the
`silo_events_client_socket_` prefix. `would_refuse_total{reason="account|process"}`
records exceeded thresholds; `refusals_total` uses bounded account, process,
draining or unavailable reasons. No account/profile/device/credential labels
are exported. A single admission can increment both would-refuse dimensions.

A maintainer must assign an operator and separately authorize representative
single-node and multi-node ordinary-use pilots. Before a later enforcement
decision, record seven consecutive days of complete coverage, including
multi-device/profile/tab use, healthy rotation, reconnect overlap and ordinary
restarts. Retain process high-water evidence across restarts. Required exit
criteria are zero account/process would-refuse events, account occupancy peak
at most 32 and process peak at most 1,024. Report draining separately. Record a
named compatible web/Android release baseline; prospective Apple generic event
consumers inherit the recovery contract. No generic Apple consumer is implemented
in the reviewed source. Independent Jellyfin clients have no coordinated release
guarantee and no implicit upgrade gate is added.

Missing coverage, exceeded thresholds or incompatible clients prevent
enforcement. Extend observation or revise the policy and repeat the seven-day
window. These gates authorize no deployment automatically. The later reviewed
change selecting enforcing mode remains a separate maintainer decision. No
capacity, pilot or human acceptance result follows from source and finite tests.

## Coordination with work-rate admission

Socket cardinality and realtime work cost are separate controls. The work-rate
admission change is separate from this implementation. The following ordering
requirements apply when integrating both controls, and do not describe behavior
provided by socket admission alone.

Prepaid ticket work is not a socket reservation; quota-denied v2 tickets and
prepaid credits stay spent. On v1 reserve before work charging. A work refusal releases a pending
reservation; an in-socket work refusal leaves the connection and reservation
intact. Upgrade precedes watcher startup and subscription. Combined verification
requires both stable heads and finite composition tests.

Generic event retry uses 5/10/20/30-second floors for failed/short attempts and a
one-second floor after an accepted subscription lasts at least 30 seconds, with
0–50% jitter. Count health from the first accepted `subscribed` acknowledgement,
not transport open. Work refusal, shield and unavailable handling takes
precedence; preserve immediate access-change recovery and identity fences. This
fallback does not apply to rooms or playback.
