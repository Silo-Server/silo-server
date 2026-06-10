# Core Push Notifications — Design

Date: 2026-06-10
Status: approved (design); implementation plan to follow

## Summary

A server-side push subsystem that mirrors the in-app notifications created in Phase 1 out to registered devices via APNs (Apple), FCM (Android), and Web Push (browsers). Delivery is durable (a DB-backed queue with a retry worker) and presence-gated (a notification only pushes to devices whose user has no live WebSocket connection after a short grace delay). This is a core-server project: the mobile clients have no push code yet, so the deliverable is the registry, queue, worker, transports, and APIs — built transport-agnostic so APNs/FCM are drop-in alongside the immediately-testable Web Push.

## Goals

- Deliver Phase-1 notifications to devices over APNs, FCM, and Web Push.
- Durable delivery: survive restarts, retry with backoff, prune dead tokens, observable in the admin task view.
- Presence-gated: don't buzz a device for a notification the user is actively seeing in-app.
- Reuse the existing per-category mute preferences; add per-device push on/off. No new per-channel matrix.
- Encrypted provider credentials via the existing settings mechanism.
- Transport-agnostic core: one `Transport` interface, three implementations, selected by device platform.

## Non-goals

- Mobile/web client integration (separate per-client projects; this ships the server contract).
- A separate in-app-vs-push preference matrix (category mutes + per-device toggle only).
- Rich push (images, action buttons) — title/body/link/category payload only in v1.
- New-device-login / security pushes beyond what the notification categories already produce.

## Architecture

New `internal/push` package and a new `internal/presence` package, plus touches to three existing seams (`notifications.Service.Create`, the events WS handler, `cmd/silo/main.go` wiring). Five independently-testable units:

1. **Device/token registry** — extends `user_devices` with push columns; a registration endpoint clients call to enroll/refresh/revoke a token and toggle per-device push. Owns "which devices want push and how to reach them."
2. **Presence registry** (`internal/presence`) — process-local `user_id → live-connection refcount`, incremented/decremented by the events WS handler on connect/disconnect. Answers "does this user have a live connection right now?" Cluster-aware via a short-TTL Redis key; degrades to per-node if Redis is absent.
3. **Enqueuer** — called from `notifications.Service.Create()` after the row commits. Resolves push-eligible devices, writes one `push_deliveries` row per device with `not_before = now + grace`.
4. **Delivery worker** — a `PushDeliveryTask` on the existing task framework; claims due rows, re-checks presence, dispatches via the platform's transport, records outcome, prunes dead tokens.
5. **Transports** — a `Transport` interface (`Send(ctx, target, payload) → Result`) with APNs, FCM, and Web Push implementations selected by device platform; credentials from the encrypted settings repo.

Data flow: `Create()` → enqueuer writes deliveries → worker (interval) claims due & un-present → transport sends → outcome / dead-token prune. The in-app path (immediate WS publish) is untouched; push is the deferred, gated mirror.

### Seam reference (current code)

- Fan-out hook: `internal/notifications/service.go` `Create()` — after `created, err := s.store.Insert(...)` succeeds and before/alongside the `hub.PublishJSON(... "notification.created" ...)` call. Enqueue is best-effort and must not block or fail `Create()`.
- Presence increment: `internal/api/handlers/events_ws.go` — after a successful subscribe, `release := presence.Add(claims.UserID)`, `defer release()`.
- No presence tracking exists today (confirmed); the registry is net-new.
- `user_devices` exists (`migrations/sql/180_user_devices.sql`, PK `user_id, profile_id, device_id`) with no token column.
- Credential encryption: `internal/catalog/encrypted_settings_repo.go` `SensitiveSettingKeys`.
- Background-worker precedent: `internal/taskmanager/tasks/*` (e.g. the request reconciler, notifications digest/retention).

## Data model

Extend `user_devices` (a push token belongs to a device):

```sql
ALTER TABLE user_devices
  ADD COLUMN push_token     text NULL,
  ADD COLUMN push_transport text NULL,   -- 'apns' | 'fcm' | 'webpush'
  ADD COLUMN push_enabled   boolean NOT NULL DEFAULT true,
  ADD COLUMN push_token_at  timestamptz NULL,
  ADD COLUMN push_failures  integer NOT NULL DEFAULT 0;
```

A device is push-eligible when `push_token IS NOT NULL AND push_enabled`. For `webpush`, `push_token` holds the JSON subscription object (endpoint + p256dh + auth); APNs/FCM store the bare token string. Each transport interprets its own format.

Delivery queue:

```sql
CREATE TABLE push_deliveries (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  notification_id bigint NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
  user_id         integer NOT NULL,
  device_id       text NOT NULL,
  transport       text NOT NULL,
  status          text NOT NULL DEFAULT 'pending',  -- pending|sent|failed|skipped|dead
  attempts        integer NOT NULL DEFAULT 0,
  not_before      timestamptz NOT NULL,
  last_error      text NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX push_deliveries_claim_idx
  ON push_deliveries (not_before)
  WHERE status IN ('pending','failed');
```

Worker claim: `status IN ('pending','failed') AND not_before <= now()`, ordered by `not_before`, `FOR UPDATE SKIP LOCKED` (safe across nodes/concurrent workers).

Outcomes:
- **sent** — terminal success.
- **skipped** — user was present (live WS) at claim time; the in-app delivery sufficed. Terminal.
- **failed** — soft error (timeout, 5xx, rate-limit); bump `attempts`, push `not_before` out by the backoff schedule (1m, 5m, 30m). Once the schedule is exhausted (4 total attempts) → `dead`.
- **dead** — hard error / unregistered token; terminal. Also clears `push_token` and sets `push_enabled=false` on the `user_devices` row so the dead device stops being enqueued.

Deliveries do not duplicate notification content — the worker joins `notifications` at send time so an edited/expired notification sends current content (and an expired/dismissed one can be skipped). Retention: terminal deliveries older than 7 days are deleted by a pass on the existing notifications retention task.

Provider config (added to `SensitiveSettingKeys`, encrypted at rest):
`push.apns.p8_key`, `push.apns.key_id`, `push.apns.team_id`, `push.apns.bundle_id`, `push.fcm.service_account_json`, `push.webpush.vapid_public`, `push.webpush.vapid_private`, `push.webpush.subject`. A transport with missing/blank config reports itself unconfigured; its deliveries short-circuit to `skipped` (reason: unconfigured) rather than erroring in a loop.

## API

Device push registration (authenticated user routes; device identified by the existing `X-Silo-Device-Id` header + `user_id` from claims — never from the body):

```
PUT    /api/v1/notifications/push/device     {transport, token}  → 204
   enroll/refresh this device's token (upsert onto the user_devices row);
   resets push_failures, sets push_enabled=true, push_token_at=now
DELETE /api/v1/notifications/push/device                         → 204
   revoke: clear token, disable push for this device
GET    /api/v1/notifications/push/devices                        → {devices:[{device_id,name,platform,push_enabled,registered_at}]}
PUT    /api/v1/notifications/push/devices/{device_id} {enabled}  → 204
   per-device on/off toggle
GET    /api/v1/notifications/push/webpush-key                    → {vapid_public_key}  (empty when web push unconfigured)
```

Admin provider config uses the existing admin settings mechanism (encrypted `push.*` keys) — no bespoke write endpoints. One read for the admin UI:

```
GET    /api/v1/admin/push/status  → {apns:{configured:bool}, fcm:{configured:bool}, webpush:{configured:bool}}
```

Status reports booleans only; secrets are never returned.

## Presence registry

`presence.Registry` interface: `Connected(userID int) bool` and `Add(userID int) (release func())`. The events WS handler calls `release := registry.Add(claims.UserID)` after a successful subscribe and `defer release()` on disconnect.

- In-process implementation: `map[int]int` refcount under a mutex.
- Cluster-aware implementation: `INCR push:presence:{userID}` in Redis with a 60s TTL refreshed on the WS heartbeat; `DECR` on release; `Connected` is key `> 0`. Redis is already used by the events hub.
- Presence failure (Redis down) fails **open** — we push — because a missed push is worse than a redundant one.

The worker re-checks `Connected` at claim time; a present user's due delivery becomes `skipped`. The `not_before` grace (default 30s) means an actively-in-app user almost never gets a redundant push, while a user who closed the app moments ago does.

## Error handling & delivery semantics

- Enqueuer is best-effort; failure to write deliveries logs and returns. `Create()` is never blocked or failed by push.
- Worker isolates per-delivery: one device's failure never blocks the batch.
- Soft errors → `failed` + exponential backoff on `not_before` (1m, 5m, 30m), then `dead` after 4 total attempts.
- Hard errors (`Unregistered`/`InvalidToken`/FCM `NOT_FOUND`) → immediate `dead` + clear token + disable push on `user_devices`.
- Unconfigured transport → its deliveries short-circuit to `skipped` (reason logged once, not per-row spam).
- Rate-limit responses park that transport briefly (honor `Retry-After`).
- Child-profile suppression applied at enqueue: categories in {request, system, admin} are never enqueued for a child profile (same rule as the web toast suppression).
- APNs/FCM payloads set a collapse/thread key = notification id to avoid duplicate banners across retries.

## Security

- Provider secrets live only in the encrypted settings repo; never logged, never returned by any read endpoint. `/admin/push/status` reports booleans.
- Token registration is authenticated and device-scoped to the caller's `user_id` (from claims).
- Push payload carries title/body/link/category + notification id — no content beyond what the notification already holds.

## Testing

- Worker tested with a fake `Transport` asserting outcome transitions: sent, skipped (present), failed-with-backoff, dead-prunes-token. No network.
- Each real transport unit-tested against a mocked HTTP endpoint (request shape, auth header, error-code mapping) — standard repo pattern.
- Enqueuer tested with a fake device store + presence stub: eligible-device resolution, child suppression, per-device disable, `not_before` grace.
- Presence registry: refcount add/release, Connected transitions, Redis-absent fallback.
- Store tests against Postgres: claim query with `SKIP LOCKED`, dead-token prune, retention.
- One integration test: `Create() → delivery row → worker → fake transport → sent`.
- Handler tests: registration auth-scoping, per-device toggle, `/admin/push/status` unconfigured states, `webpush-key` empty when unconfigured.

## Implementation sequencing

1. Migration (user_devices columns + push_deliveries) and domain types.
2. `internal/presence` registry (in-process + Redis) and WS-handler wiring.
3. Device registry store + registration/toggle endpoints.
4. Enqueuer + hook into `Create()`.
5. `Transport` interface + fake; delivery worker + task registration; outcome/backoff/dead-prune.
6. Real transports (Web Push first — end-to-end testable today; then APNs, then FCM) + encrypted config keys + `/admin/push/status`.
7. Retention pass + full verification.

## Future work

- Client integration: Apple (APNs token registration + handling), Android (FCM), web (service worker + VAPID subscription). Each binds to the frozen registration API.
- Presence-aware in-app→push escalation tuning (per-category grace, quiet hours).
- Rich push (images from `item_id` artwork, action buttons).
- Per-channel preference matrix if category+device proves too coarse.
