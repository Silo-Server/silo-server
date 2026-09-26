# API-key authority and usage acceptance

Run the bounded frozen packet with:

```sh
SILO_SCENARIO_REQUIRED=1 go test -count=1 -run '^TestRequiredKeyAuthorityUsageAcceptance$' ./internal/scenariocatalog/executor
```

`SILO_SCENARIO_DATABASE_URL` must identify an exclusively owned disposable
PostgreSQL database with the project's extensions. `SILO_SCENARIO_REPORT` can
capture the per-transport report. The runner refuses missing configuration,
non-fixture data and foreign API keys before constructing the environment.
Never point this destructive fixture runner at a shared or deployed database.

The packet selects nine original cases: key-list API-key refusal and public
error shape, key-create and key-delete API-key refusals, API-key scope discovery,
unscoped and scoped account reads, API-key logout, and scoped invitation-list
refusal. The embedded original inventory preserves every request, principal,
requirement and response assertion. V2 adds its exact operation and response
oracle; it does not replace the original transport's assertions. Key-list
meaning/shape security-removal questions are outside this packet.

API-key authentication schedules a last-used write after credential and scope
admission, even if the handler then refuses key management or logout. Scoped
refusals and missing credentials stop before that producer. The real tracker
coalesces writes per key for a minute. A fresh production router per exchange
prevents coalescer state from crossing fixture reseeds that reuse numeric key
IDs; no production clock, updater or authentication implementation is replaced.
This packet does not establish retry or repeated-use coalescing behavior.

A disposable database trigger records actual `last_used_at` updates and emits a
transactional notification. The runner closes the HTTP producer and waits for
the admitted key's committed update before observing effects or reseeding.
The producer has one bounded goroutine and no retry queue. Failure to observe
its commit aborts further reseeding. Public and scope-refusal paths have no
producer to drain. No fixed sleeps or blanket timestamp normalization are used.

Eighteen original exchanges produce 36 full eight-table snapshots. Twelve
admitted-key exchanges must each change only that key's `last_used_at`, from
null to an instant within database clocks surrounding the request and drain.
All other key columns/rows and all users, profiles, settings, login sessions,
device requests, invitations and invite codes must remain byte-identical.
The other six exchanges must leave every observed column unchanged. The
observer records exactly one or zero commits respectively. Observation objects
are removed and application fixtures restored on successful completion.

The companion selector tests reject missing/duplicate cases, altered original
oracles or authority, and changed transport bindings. Positive live results do
not establish provider, enrollment, native, concurrent-use or outage behavior.
