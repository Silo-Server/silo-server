# Server-Sent Events stream for the events hub — design

Date: 2026-07-31
Status: approved, ready for implementation planning

## Problem

Silo's realtime events hub is reachable only over WebSocket
(`GET /api/v1/events/ws`). That transport is a poor fit for external
integrations:

- It requires a `subscribe` frame within 5 seconds or the server closes the
  connection with a policy violation. A client that connects and waits silently
  receives nothing and never learns why.
- Consumers must implement handshake, ping/pong, reconnect and backoff
  themselves.
- Ecosystem tooling in this space is overwhelmingly SSE-based. Tracearr, a
  monitoring dashboard for self-hosted media servers, drives Plex, Jellyfin and
  Emby entirely over SSE and has no WebSocket client at all; supporting Silo
  currently forces a new transport and a new runtime dependency into it.

The hub itself is transport-agnostic — `events.Hub.Subscribe()` returns a plain
channel of envelopes. Only the delivery layer is WebSocket-bound.

## Approach

Add a read-only SSE transport over the existing hub at

    GET /api/v1/events/sse

It reuses the hub, the role-based channel allow-list, and the per-event
authorisation check that the WebSocket handler already applies. No new event
plumbing, no new event types, no change to publishing.

### Why not extend the WebSocket handler

Nothing about the WebSocket path is wrong for first-party clients — the Android,
Apple and web clients use it and benefit from bidirectional control. SSE is
added alongside it for consumers that only need to observe. Both remain
supported.

### Endpoint

`GET /api/v1/events/sse?channels=sessions,jobs`

- **Auth:** the same bearer credential as the rest of `/api/v1`. Unlike the
  WebSocket path, SSE requests can set an `Authorization` header, so the
  ws-ticket mechanism is unnecessary and profile-scoped channels that depend on
  it are not offered here.
- **Channel selection is a query parameter, not a handshake.** SSE is one-way,
  so there is nothing to negotiate. Omitting `channels` subscribes to every
  channel the caller's role allows. Requesting a channel the role does not allow
  omits it silently rather than failing the request — a caller asking for one
  forbidden channel among several should still get the rest.
- **No subscribe deadline.** The 5-second rule exists only because the WebSocket
  handler cannot otherwise distinguish a live client from a stalled one; SSE has
  no such ambiguity.

### Frames

```
event: hello
data: {"type":"hello","schema_version":1,"connection_id":"01K…","available_channels":["sessions",…],"required_action":"none"}

event: sessions
data: {"channel":"sessions","event":"sessions.replaced","data":[…]}

event: ping
data: {"type":"ping","ts":"2026-07-31T10:00:00Z"}
```

- `hello` is sent immediately on connect, mirroring the WebSocket hello but with
  `required_action: "none"`.
- Each hub envelope is written as a named event whose name is its channel, so a
  consumer can use `addEventListener('sessions', …)` and ignore the rest.
- `ping` is emitted on a fixed interval as a keepalive, since idle SSE
  connections are otherwise vulnerable to proxy timeouts.
- Standard SSE framing throughout: `event:`/`data:` lines, blank-line
  terminated, `data` always a single line of compact JSON.

### Headers

`Content-Type: text/event-stream`, `Cache-Control: no-cache`,
`Connection: keep-alive`, `X-Accel-Buffering: no` (openresty fronts several Silo
deployments and will otherwise buffer the stream indefinitely).

### Backpressure

The hub gives each subscriber a buffered channel and drops for slow consumers.
The SSE writer flushes after every frame and never blocks on a slow client
beyond the write itself; a consumer that cannot keep up loses events rather than
stalling the hub, which matches the WebSocket path's existing behaviour.

## Capability discovery

`docs/` requires new features to be discoverable rather than version-sniffed. The
existing capability endpoint gains a flag advertising the SSE stream and its
schema version, so a client can detect support without probing the URL or
comparing release numbers.

## Testing

- Handler tests using `httptest`: hello frame shape, an event written for a
  subscribed channel, an event suppressed for an unsubscribed one, a
  role-forbidden channel omitted rather than erroring, malformed `channels`
  values ignored, unauthenticated request rejected.
- A test asserting the response headers, including `X-Accel-Buffering`.
- A test that the writer flushes per frame rather than buffering to completion.
- Existing WebSocket tests must continue to pass untouched — this adds a
  transport, it does not modify one.

## Out of scope

- **Profile-scoped notification channels.** They depend on the ws-ticket binding
  and are deliberately not offered over SSE.
- **Any change to the WebSocket handler**, the hub, or event publishing.
- **Client-to-server messages.** SSE is one-way by construction; consumers
  needing control use the WebSocket path.
