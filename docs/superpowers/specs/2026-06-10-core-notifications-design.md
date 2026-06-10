# Core Notifications System — Design

Date: 2026-06-10
Status: approved (design); implementation plan to follow

## Summary

A first-party, whole-system notification layer for Silo: persisted per-user notifications with an inbox, read state, per-category preferences, real-time toasts over the existing event WebSocket, and admin announcements. Ships across the server, web UI, and both mobile apps (in-app only — no APNs/FCM push in this project). External delivery (Discord, email, webhooks) remains the job of the existing `silo.notifications` plugin, which consumes the same events.

## Goals

- Persisted per-user notifications: inbox, unread badge, read/dismiss state.
- Real-time in-app delivery to connected clients via the existing `events.Hub` / `/api/v1/events/ws` pipeline.
- Five categories: `request`, `content`, `announcement`, `system`, `admin` (operational alerts for admin users), plus opt-in `content_digest`.
- Per-category user preferences (announcements not mutable).
- Admin announcements with audience targeting and expiry.
- A documented `notifications.send` event contract so any plugin can create in-app notifications (and, via the existing external plugin's subscription to the same event, external ones).
- Core request lifecycle events (`request.*`) — net-new publishing from `internal/requests`, consumed by notifications and available to the requests UI.

## Non-goals

- Mobile push (APNs/FCM) — future project; nothing here blocks it.
- Request-system unification (`request_router.v1`, migrating ebook/audiobook request plugins onto `media_requests`) — separate future project. When it lands, the book plugins' `notifications.send` publishes are replaced by core `request.*` events with no notifications schema/API change.
- Absorbing external delivery channels into core.
- Fine-grained per-show/per-author subscription rules (watchlist/favorites are the v1 signal).

## Architecture

New package `internal/notifications` in silo-server:

1. **Materializer** — in-process consumer registered on `events.Hub` at startup (same pattern as `internal/plugins/event_dispatcher.go`). Runs events through per-category matchers; writes `notifications` rows for each recipient.
2. **Store** — Postgres repository: insert (idempotent), list (cursor), unread count, mark read, dismiss, preferences CRUD.
3. **Notifier** — after insert, publishes `notification.created` on a new `ChannelNotifications` event channel, user-scoped like `ChannelUserState`. The WS handler (`internal/api/handlers/events_ws.go`) adds this channel to the per-user subscribe set; frames carry the full notification object. Subscribe-time snapshot carries unread count only.
4. **API handlers** — REST endpoints below.
5. **Service API** — `notifications.Create(...)` for direct in-process callers (system/security events).

Creation is DB-first and synchronous: a notification exists when the row commits. WS fan-out is best-effort; clients that miss frames reconcile on next fetch. No delivery queue or retry in core — "delivery" is reading your own table.

## Data model

One migration, three tables:

```sql
notifications (
  id            bigserial PRIMARY KEY,
  user_id       int NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  profile_id    text NULL REFERENCES user_profiles(id) ON DELETE CASCADE,  -- NULL = user-wide
  category      text NOT NULL,  -- 'request' | 'content' | 'announcement' | 'system' | 'admin'
  type          text NOT NULL,  -- e.g. 'request.approved', 'content.watchlist_episode'
  title         text NOT NULL,
  body          text NOT NULL,
  link          text NULL,      -- in-app deep link
  item_id       bigint NULL,    -- optional content reference (artwork, dedup)
  source_event  text NULL,      -- originating event name (audit)
  dedup_ref     text NULL,      -- idempotency: UNIQUE (user_id, type, dedup_ref) WHERE dedup_ref IS NOT NULL
  created_at    timestamptz NOT NULL DEFAULT now(),
  read_at       timestamptz NULL,
  dismissed_at  timestamptz NULL,
  expires_at    timestamptz NULL
);
-- index: (user_id, created_at DESC) partial WHERE dismissed_at IS NULL
-- partial unique index for dedup as above; inserts use ON CONFLICT DO NOTHING

notification_preferences (
  user_id   int NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  category  text NOT NULL,  -- 'request' | 'content' | 'system' | 'admin' | 'content_digest'
  enabled   bool NOT NULL,
  PRIMARY KEY (user_id, category)
);
-- absent row = enabled, except content_digest which defaults OFF
-- 'announcement' is not a valid category here (not mutable)

announcements (
  id          bigserial PRIMARY KEY,
  title       text NOT NULL,
  body        text NOT NULL,
  audience    jsonb NOT NULL,  -- {"all":true} | {"user_ids":[...]} | {"library_ids":[...]}
  created_by  int REFERENCES users(id),
  created_at  timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NULL
);
```

Announcements fan out to `notifications` rows at publish time (audience resolved then), so the inbox is always a single-table query and read state is per-user.

Child profiles: rows with `category IN ('request','system','admin')` are filtered out of child-profile sessions server-side; content matching never targets a child profile unless the matched watchlist/favorite belongs to that profile.

Retention (nightly job, existing scheduler pattern): dismissed rows purged after 30 days; all rows after 90 days; unread rows from expired announcements at expiry.

## API

User endpoints (authenticated, profile from claims):

```
GET    /api/v1/notifications              ?unread=1&category=&cursor=&limit=
GET    /api/v1/notifications/unread-count → {count}
POST   /api/v1/notifications/read         {ids:[...]} | {all:true}
POST   /api/v1/notifications/{id}/dismiss
GET    /api/v1/notifications/preferences
PUT    /api/v1/notifications/preferences  {category: enabled, ...}
```

List returns user-wide rows plus active-profile rows, newest first, cursor-paginated in the standard envelope. Child-profile filtering server-side.

Admin endpoints (RequireAdmin group):

```
GET    /api/v1/admin/announcements
POST   /api/v1/admin/announcements        {title, body, audience, expires_at?}
DELETE /api/v1/admin/announcements/{id}   -- also dismisses unread fanned-out rows
```

## Events

### New: core request lifecycle events

`internal/requests/service.go` and the reconciliation task publish on a new `ChannelRequests`:

- `request.submitted`, `request.approved`, `request.declined`, `request.completed`, `request.failed`
- Payload: `request_id, user_id, profile_id, title, media_type, quality, status`

`ChannelRequests` is added to WS subscribe sets (requesting user + admins), giving the requests UI live updates as a side benefit. This is the only change to the requests subsystem in this project.

### New: `notifications.send` plugin contract

The materializer consumes `notifications.send` events:

```json
{ "user_id": 1, "profile_id": null, "category": "request",
  "type": "request.fulfilled", "title": "...", "body": "...",
  "link": null, "dedup_ref": "ebook-req-123:fulfilled" }
```

Payloads are validated; malformed events are dropped with a logged warning. The external `silo.notifications` plugin already subscribes to this event name, so one publish reaches both in-app and external channels. The ebook/audiobook request plugins each add one `PublishEvent("notifications.send", ...)` at their acknowledged/fulfilled/failed transitions (their existing status events do not carry `user_id`; the plugins own that context).

### Matchers

- **request** — consumes core `request.*` events only. Dedup ref `(request_id, status)`.
- **content** — consumes `library.item_added` / `catalog.library.changed`. For each added item: resolve series/author; match per profile against watchlist, favorites (author/series link), and in-progress series (last play within 21 days); union, dedup, filter by library access. Burst guard: one notification per series per profile per hour (season imports collapse).
- **content_digest** — nightly job (not event-driven), opt-in: one row per user summarizing the previous day's additions to accessible libraries.
- **system** — not matched from the hub; emitted via `notifications.Create()` directly at the source (password change, new-device login, guest-pass redemption). Sites also publish their events as today; the explicit call means security notices never depend on matcher wiring.
- **announcement** — fan-out at publish time.
- **admin** — consumes existing `ChannelJobs` / `ChannelScans` / `ChannelTasks` failure events plus plugin-host crash/install failures; targets admin users; repeat-failure throttle (same source, max one per hour).

Preference checks run in the materializer before insert (cached per-user prefs lookup).

## Web UI

1. **Bell + dropdown** in the top bar: unread badge from `unread-count`, kept live by `ChannelNotifications` frames via the existing `RealtimeEventsProvider` socket. Dropdown: latest ~10, mark-all-read, deep links.
2. **Toasts** — WS notification frames fire sonner toasts; suppressed while the dropdown is open or when the frame targets another profile.
3. **Inbox page** (`/notifications`) — paginated list, category filter tabs, dismiss, mark-read on view.
4. **Settings** — per-category toggles (`content_digest` default off; `admin` visible to admins only). Admin: Announcements page (list/compose/expire) under admin settings.

## Mobile (in-app, both platforms)

Same three features in silo-apple and silo-android against the frozen REST API: inbox screen (list, mark-read, dismiss), unread badge on existing navigation, preferences screen. Transport: REST refresh of unread-count + first page on app foreground and pull-to-refresh. No WS subscription in v1 (current app sockets are playback-scoped; no background delivery exists without push anyway). TV variants: badge + read-only inbox, no preferences editing.

## Error handling

- Matcher isolation: a panic/error in one matcher logs and skips that event for that matcher only; never blocks the hub or other matchers (same containment as the plugin event dispatcher).
- Idempotent inserts via the dedup partial unique index + `ON CONFLICT DO NOTHING`; replayed events cannot double-notify.
- WS fan-out is fire-and-forget; DB rows are the source of truth.
- `notifications.send` validation failures: drop + warn (plugin bugs must not break core paths).

## Testing

- Unit tests per matcher with synthetic events; content-matcher edge cases: child profiles, library access, burst guard, dedup.
- Store tests against Postgres (repo convention).
- One integration test: event → materializer → row → WS frame through a real hub.
- API handler tests: auth, profile scoping, child filtering, admin gates.
- Web: component tests for bell/inbox; extend the RealtimeEventsProvider test pattern for the new channel.

## Implementation sequencing

1. **Server** — migration, store, materializer + matchers, request event publishing, API, WS channel, retention job.
2. **Web** — bell, toasts, inbox, settings, announcements admin.
3. **Mobile** — apple + android in-app surfaces against the frozen API.
4. (Separate projects, later: request unification via `request_router.v1`; mobile push.)

## Future work

- Request unification: core `request.*` events replace the book plugins' `notifications.send` publishes; notifications unchanged.
- Push: APNs/FCM device registry; the notifier gains a push fan-out alongside WS.
- Digest scheduling preferences (weekly, time-of-day).
- Profile claims enrichment: embed `is_child` in profile-scoped auth claims. Fixes two implementation-time findings: the WS pipeline cannot child-filter (snapshot and live frames use client-asserted profile identity; REST remains authoritative and fails closed — see the snapshot comment in `internal/api/handlers/events_ws.go`), and the REST `childSafe` check costs one profile lookup per request.
- Admin-alert coverage: only `job.failed` and `scan.failed` events exist today. `task.failed` and plugin-host crash events are not published by their subsystems yet; when they are, the admin matcher needs only a map entry.
- Dedicated `item_added` catalog event: the content matcher keys on `catalog.item.changed` (`metadata_updated`) plus a 48-hour `media_items.created_at` recency gate, because the scanner publishes no per-item add event. An explicit ingest-time event would remove the proxy heuristic.
- New-device login notifications: deferred from v1 (no device-fingerprint store exists); password-change notifications shipped.
