# Handoff: `account.capabilities_changed` event on the events WebSocket

**Date:** 2026-07-06
**Requested by:** Apple client team (silo-apple)
**Status:** Proposed — server implementation needed

## Problem

Clients cache user-facing capability payloads (e.g. `GET /api/v1/downloads/capability`)
and have no way to learn that an admin changed something that affects them. Concrete
incident: enabling "Allow transcoded downloads" (`users.download_transcode_allowed`)
for a user did not surface the transcode quality presets in the iOS app until the
client's cache expired (previously 24h).

The Apple client now re-fetches capability on app-foreground and when the download
options UI opens (pull-based safety net, shipped in silo-apple). This handoff covers
the push half: an invalidation event so connected clients react immediately, without
polling.

## Existing infrastructure (no new transport needed)

- Events WebSocket: `GET /api/v1/events/ws` (`internal/api/handlers/events_ws.go`),
  channel-based subscribe with per-role channel ACLs (`allowedChannelsForRole`).
- `user_state` channel is already available to non-admin users and the hub already
  filters envelopes by `env.UserID` for non-admins (`allowsEventForClaims`), so a
  user-targeted event on this channel is only delivered to that user (and admins).
- Publishing helper: `evt.Hub.PublishJSON(ctx, channel, type, payload, opts)`
  (`internal/events/hub.go`), pattern example in
  `internal/api/handlers/user_state_events.go`.

## Proposed contract

**Channel:** `user_state` (reuse; it is the only user-scoped channel non-admins can
subscribe to, and clients that care already subscribe to it).

**Event type:** `account.capabilities_changed`

**Payload — intentionally minimal (invalidation ping, not a data carrier):**

```json
{
  "scope": "downloads" | "playback" | "all"
}
```

Do **not** include the new capability values. Clients respond by re-fetching the REST
endpoints they already consume (`/api/v1/downloads/capability`, playback prefs, etc.).
This keeps the event contract trivial and guarantees it never drifts from the REST
response shapes. `scope` is a coarse hint so clients can skip irrelevant re-fetches;
when in doubt publish `"all"`.

**Targeting:**

- Per-user change → `PublishOptions{UserID: <affected user>}` so only that user's
  connections receive it.
- Server-wide setting change → publish once with no `UserID` (fan-out to all
  connected users). Verify `allowsEventForClaims` passes envelopes with
  `UserID == 0` to non-admins — it does today (`env.UserID > 0` guard).

## Publish sites

1. **Admin user update handlers** (`internal/api/handlers/admin.go` user
   create/update paths): publish scope `"downloads"` when any of
   `download_allowed`, `download_transcode_allowed` change; `"playback"` for
   playback-affecting fields (`max_streams`, `max_transcodes`, quality limits);
   `"all"` if simpler. Target the affected `UserID`.
2. **Server settings writes** (settings handler / config store): publish untargeted
   scope `"downloads"` when any `download.*` key changes (notably
   `download.transcode_enabled`, `download.enabled`, `defaults.download_*`);
   `"playback"` for `playback.*` gates that shape client-visible capability.
3. **Access-group / policy-engine changes** that alter `download` /
   `download_transcode` action outcomes, if applicable — same event, `"all"` scope,
   untargeted (membership makes per-user targeting fiddly; the re-fetch is cheap).

No snapshot frame needed for this event type (`snapshotForChannel` for `user_state`
already returns `null`).

## Client behavior (for reference / Android alignment)

- Apple: will add a `ServerEventsClient` subscribing to `user_state` +
  `notifications`; on `account.capabilities_changed` with scope `downloads`/`all`,
  calls its existing capability re-fetch. Until then the pull-based refresh already
  covers correctness.
- Android: already has `NotificationsRealtimeClient` on this socket; should handle
  the same event the same way.
- Clients must treat the event as best-effort: sockets are down while backgrounded,
  so they keep refresh-on-foreground / refresh-on-use regardless.

## Acceptance checks

1. Toggle `download_transcode_allowed` for a connected non-admin user → that user's
   socket receives `account.capabilities_changed` (scope `downloads`); other users
   receive nothing.
2. Flip `download.transcode_enabled` server setting → all connected users receive
   the event once.
3. A non-admin subscribed to `user_state` never receives another user's targeted
   capability events (existing `allowsEventForClaims` filtering).
4. `GET /api/v1/downloads/capability` immediately after the event reflects the new
   presets (it already does — the event carries no data to go stale).
