# Plugin event delivery

`plugins.EventDispatcher` (`internal/plugins/event_dispatcher.go`) delivers
host events to plugin `event_consumer.v1` capabilities whose manifest
`subscriptions` list the event's name. Once an event is stored, it reaches
each subscribing installation at least once across the cluster unless every
attempt fails, with a stable `event_id` that plugins can deduplicate on. One
plugin process handles each attempt, on whichever API server claimed it.
Deliveries are not ordered.

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

Both paths check the subscriber index before queueing, so events no plugin
subscribes to cost a map lookup and never take a place in the intake queue.
A bus event gets a new ULID as its `event_id`. An envelope keeps its
`EventID`.

## Outbox

The producing server writes one `plugin_event_deliveries` row per
subscribing installation and wakes its delivery loop. Every API server runs
that loop. It claims due rows with `FOR UPDATE SKIP LOCKED` inside a
`MATERIALIZED` CTE, which counts the attempt and pushes `next_attempt_at` out
by a two-minute lease. It then calls `HandleEvent` on its own process for the
installation. Starting the plugin may take up to a minute, and the call has
the host's 10-second event timeout.

| Outcome | Row |
| --- | --- |
| Success | Deleted, even when the server began shutting down after the plugin answered. |
| Error, timeout, or the installation disabled or gone on this server | Due again after 10 s, 30 s, 1 min, 3 min, then 15 min. |
| Error after the last delay | Deleted and logged. |
| A payload that is not a JSON object | Deleted and logged. |
| The server dies or shuts down mid-delivery | Claimed again by any server once the lease runs out. |

Uninstalling a plugin deletes its rows through the foreign key, and a row for
an installation uninstalled since the producer listed its subscribers is
skipped rather than failing the other rows' insert. A disabled
installation is retried rather than dropped because the delivering server's
installation cache can lag an enable made on another server.

Besides being woken, each server polls for due rows every 10 seconds. That
picks up retries, rows whose producer had no free delivery slot, and rows
whose claimer died. `Stop` writes the events still queued, including those
published while the server drains its requests after its context ended.

## Invariants

- Never deliver plugin events from a bus subscription or from relayed hub
  envelopes. Every node receives those, and each would deliver the event
  again.
- A claim must stay a `MATERIALIZED` CTE. As
  `id IN (SELECT ... LIMIT n FOR UPDATE SKIP LOCKED)` the planner may run the
  subquery more than once and claim more than `n` rows.
- The lease outlasts the delivery timeout, or a slow delivery is claimed by a
  second server while the first is still running. Starting a plugin is shared
  by its callers and ignores their deadlines, so the dispatcher stops waiting
  for it at its own deadline.
- Finishing a delivery is guarded by the attempt count its claim returned. A
  server whose lease ran out and whose row another server claimed since
  neither deletes nor reschedules that row.
- `payload` is `json`, not `jsonb`: `jsonb` refuses a `\u0000` escape, and one
  such payload would fail the whole insert for every subscriber.

## Limits

- An event is lost before it is stored when its server dies first, when the
  intake queue (1024 events) is full, or when the outbox write fails. A full
  queue drops bus events with a log line. Hub envelopes wait in the hub's
  64-envelope buffer for the dispatcher, and `Hub.Dropped` counts the ones it
  drops; once the server's context has ended, they are dropped with a log
  line too.
- Each server delivers four events at a time across all plugins. A plugin
  that hangs holds a slot for 10 seconds per attempt, or a minute when it
  will not start, so its backlog delays other plugins' events.
- A server stores the events it produces by its own subscriber index. After
  a plugin is installed or enabled on another server, it learns of the change
  from `plugins_changed`, which that server publishes before its slower
  lifecycle hooks. Events it produces in the meantime, a Redis round trip or
  up to a minute when the message is lost, miss the new subscriber.
- One process sees each event, so a plugin that keeps per-process state
  cannot use events to keep every process's copy current.
- During a rolling upgrade, servers on older builds still deliver from their
  bus and hub subscriptions, once per server, without retries and without an
  `event_id`, until they are replaced.

## Plugin contract

The SDK documents what plugin authors can rely on: `HandleEventRequest.event_id`
and the "Receiving events" section of `silo-plugin-sdk`'s
`docs/runtime-host.md`.
