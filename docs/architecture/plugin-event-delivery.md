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

The dispatcher reads envelopes published on the `events.Hub`. Each hub also
relays other nodes' envelopes to its local subscribers; the dispatcher takes
only envelopes whose `SourceID` is its own hub's, and skips `V2Only` ones.
`RuntimeHost.PublishEvent` puts plugin-published events on the hub too.

It does not read the [Redis event bus](redis-event-bus.md). Bus event types
are internal cache and coordination signals, not a plugin contract, and
Redis hands every message to every node.

## Which events plugins receive

Plugins receive only the events in the SDK's catalog
(`silo-plugin-sdk` `pkg/pluginsdk/events`, documented in its `docs/events.md`)
and plugin events (`plugin.<plugin_id>.<name>`). The subscriber index leaves
out any other name a manifest lists, so an internal event such as
`user_state.changed` never reaches a plugin.

Before an event is stored, `events.Project` rewrites its payload into the
catalog's public shape: the fields of the SDK payload type and nothing else.
A field the host adds for its own clients therefore stays off the plugin
contract until the SDK type adds it. `notification.created` also gets the
envelope's `user_id`, because profile IDs repeat across accounts.

Request lifecycle events (`request.submitted`, `.approved`, `.declined`,
`.fulfilled`) exist only for plugins: `notifications.System` publishes them
on `ChannelPlugins`, which no websocket client receives, beside the
server-channel post for the same transition. Their event ID is derived from
the request and its last update, so a repeated call carries the same ID.

`TestEventCatalogMatchesProducers` projects what each producer publishes and
fails when a producer change drops a catalog field, or when the SDK catalog
gains an event without a producer case.

The index check happens before queueing, so events no plugin subscribes to
cost a map lookup and never take a place in the intake queue. An envelope
keeps its `EventID`.

## Gating by approved access

Every catalog event has an access class (`events.AccessOf`): `public`,
`admin` or `user_data`. Plugin events are `public`. The subscriber index
includes a subscriber for an `admin` or `user_data` event only when its
installation's `plugin_permission_grants` row approves that class; public and
plugin events need no approval. An installation without a grant receives only
those. `buildIndex` reads every grant in one query with the enabled
installations, so gating costs nothing per event.

An approval runs `Service.OnLifecycleChange`, which drops this server's index
and publishes `plugins_changed`, so every API server rebuilds its index on the
next event. It restarts no plugin. Installing, the boot pass and approvals are
the only writers of approved access;
[plugin-permissions.md](plugin-permissions.md) covers when each one runs.

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
- Only catalog and plugin events reach plugins, in their projected shape.
  Delivering another name, or a payload that skipped `events.Project`, makes
  an internal detail part of the plugin contract.
- An `admin` or `user_data` event reaches only installations whose grant
  approves its class. The check lives in the index, so a stored delivery was
  approved when its event was produced.
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
  intake queue (1024 events) is full, or when the outbox write fails.
  Envelopes wait in the hub's 64-envelope buffer for the dispatcher, and
  `Hub.Dropped` counts the ones it drops; once the server's context has
  ended, they are dropped with a log line.
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
- During a rolling upgrade, servers on older builds still deliver internal
  event names and unprojected payloads, and ignore grants, until they are
  replaced.
- Gating applies when an event is stored. Deliveries already stored when an
  approval narrows a grant are still delivered.

## Plugin contract

The SDK documents what plugin authors can rely on: the event catalog and
payloads in `silo-plugin-sdk`'s `docs/events.md`, `HandleEventRequest.event_id`,
and the "Receiving events" section of its `docs/runtime-host.md`.
