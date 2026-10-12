# Plugin event delivery

`plugins.EventDispatcher` (`internal/plugins/event_dispatcher.go`) delivers
host events to plugin `event_consumer.v1` capabilities whose manifest
`subscriptions` list the event's name. Each event reaches each subscribing
installation at least once across the cluster, with a stable `event_id` that
plugins can deduplicate on. Deliveries are not ordered.

## Where events come from

Only the API server that produced an event stores it, so the number of API
servers does not change how often a plugin hears of it.

- Host code publishes catalog, admin and playback events on the
  [Redis event bus](redis-event-bus.md). Redis hands every message to every
  node, so the dispatcher does not subscribe to those channels. `main.go`
  wraps the bus in `cache.ObservedEventBus` and registers
  `EventDispatcher.ObservePublish`, which sees each event the process itself
  publishes. Without Redis the bus delivers nothing, and the observer still
  runs.
- Envelopes published on the `events.Hub`, which is where
  `RuntimeHost.PublishEvent` puts plugin-published events. Each hub also
  relays other nodes' envelopes to its local subscribers; the dispatcher
  takes only envelopes whose `SourceID` is its own hub's.

A bus event gets a new ULID as its `event_id`. An envelope keeps its
`EventID`.

## Outbox

The producing server writes one `plugin_event_deliveries` row per
subscribing installation and wakes its delivery loop. Every API server runs
that loop. It claims due rows with `FOR UPDATE SKIP LOCKED` inside a
`MATERIALIZED` CTE, which counts the attempt and pushes `next_attempt_at` out
by a two-minute lease. It then calls `HandleEvent` on its own process for the
installation, with one minute for the client and the call together.

| Outcome | Row |
| --- | --- |
| Success | Deleted. |
| Installation disabled or gone | Deleted without retrying. Uninstalling also deletes the rows through the foreign key. |
| Error or timeout | Due again after 10 s, 30 s, 1 min, 3 min, then 15 min. |
| Error after the last delay | Deleted and logged. |
| The server dies or shuts down mid-delivery | Claimed again by any server once the lease runs out. |

Besides being woken, each server polls for due rows every 10 seconds. That
picks up retries, rows whose producer had no free delivery slot, and rows
whose claimer died.

## Invariants

- Never deliver plugin events from a bus subscription or from relayed hub
  envelopes. Every node receives those, and each would deliver the event
  again.
- A claim must stay a `MATERIALIZED` CTE. As
  `id IN (SELECT ... LIMIT n FOR UPDATE SKIP LOCKED)` the planner may run the
  subquery more than once and claim more than `n` rows.
- The lease outlasts the delivery timeout, or a slow delivery is claimed by a
  second server while the first is still running.
- Finishing a delivery is guarded by the attempt count its claim returned. A
  server whose lease ran out and whose row another server claimed since
  neither deletes nor reschedules that row.
- An event is stored only by the server that produced it. If that server
  dies before the row is written, plugins do not get the event; the intake
  queue holds at most 1024 events, and a bus publish that finds it full is
  dropped and logged.
- During a rolling upgrade, servers on older builds still deliver from their
  bus and hub subscriptions, once per server and without retries, until they
  are replaced.

## Plugin contract

The SDK documents what plugin authors can rely on: `HandleEventRequest.event_id`
and the "Receiving events" section of `silo-plugin-sdk`'s
`docs/runtime-host.md`.
