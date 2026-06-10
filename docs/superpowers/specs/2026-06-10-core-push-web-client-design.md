# Core Push — Web Client & Admin Config — Design

Date: 2026-06-10
Status: approved (design); implementation plan to follow

## Summary

The core-buildable slice of Phase 3 (client work): a Web Push browser client and an admin push-provider config UI, both living in the silo-server web app plus one ~10-line Go endpoint. This makes the push subsystem operable end to end from the browser — an operator enters provider credentials in an admin page, a user enables push on their browser, and notifications arrive as OS banners with the app closed. The mobile clients (silo-apple APNs, silo-android FCM) remain out of scope; they are separate per-repo projects that consume the same frozen registration API.

This delivers user-facing notification delivery to browsers. It is not a plugin-communication channel; plugins relate to notifications only via the existing `notifications.send` event contract (a plugin produces a user notification, which then flows to in-app + push).

## Goals

- Web Push delivery to browsers: service worker, subscription, receive, click-through.
- A per-device "push on this device" control and a device-management list in user settings.
- An admin page to configure APNs / FCM / Web Push provider credentials with readiness status.
- VAPID key generation from the admin UI.
- Reuse everything already built: the frozen push registration API, the encrypted settings write path, the Phase-2 notifications settings page and api client.

## Non-goals

- Mobile client integration (silo-apple / silo-android) — separate projects against the frozen API.
- A PWA framework / offline caching (vite-plugin-pwa) — a static service worker covers push; offline is out of scope.
- Any change to the push delivery worker, transports, or registration API (all frozen).
- Rich push (images, action buttons).

## Frozen dependencies (already on this branch)

Push registration API (`internal/api/handlers/push.go`, routes in `router.go`):

```
PUT    /api/v1/notifications/push/device     {transport, token}  → 204   (RequireProfile)
DELETE /api/v1/notifications/push/device                         → 204   (RequireProfile)
GET    /api/v1/notifications/push/devices                        → {devices:[DeviceInfo]}
PUT    /api/v1/notifications/push/devices/{device_id} {enabled}  → 204
GET    /api/v1/notifications/push/webpush-key                    → {vapid_public_key}
GET    /api/v1/admin/push/status                                 → {apns,fcm,webpush: bool}
```

`DeviceInfo`: `{device_id, name, platform, transport, push_enabled, registered_at}`. The api client (`web/src/api/client.ts`) auto-attaches `X-Silo-Device-Id` and `X-Profile-Id`. WebPush tokens are stored as the JSON subscription object.

Admin settings write path: `PUT /api/v1/admin/settings/{key}` `{value}` (`admin.go` HandleUpdateSetting) — all `push.*` keys are pre-registered sensitive in `encrypted_settings_repo.go` (encrypted at rest, value redacted from reads). No new write endpoint needed. Sensitive-status hook (`useAdminSensitiveStatus`) reports which keys are stored.

Web push server transport already sends payload `{id, title, body, link, category}` with `Topic: "n"+base36(notificationID)` for collapse.

## Architecture

Six units (five web, one tiny Go endpoint):

1. **Service worker** — `web/public/sw.js` (static). `push` → `showNotification` from payload; `notificationclick` → focus an open app tab (postMessage the link) or `openWindow(link)`; `pushsubscriptionchange` → re-subscribe with the cached VAPID key and re-register (best-effort token refresh).
2. **SW registration** — in `web/src/main.tsx`, registers `/sw.js` when `serviceWorker` + `PushManager` are supported; no-op otherwise.
3. **`usePushDevice` hook** — `web/src/hooks/usePushDevice.ts`. Owns the browser-push lifecycle and exposes a status state machine + `enable()`/`disable()`.
4. **Settings UI** — extend `web/src/pages/settings/NotificationsSettings.tsx`: "Push on this device" control + a device-management list (per-device enable/disable).
5. **Admin push-config page** — `web/src/pages/admin-settings/PushSettings.tsx`: three credential cards + VAPID keygen + readiness badges.
6. **VAPID keygen endpoint** — `POST /api/v1/admin/push/generate-vapid-keys` (RequireAdmin), ~10 lines in `push.go`. The only net-new server code.

## Push client data flow

`usePushDevice` reports `status`:
- **unsupported** — no serviceWorker/PushManager, or iOS Safari not installed as a PWA. UI explains; no action.
- **blocked** — `Notification.permission === "denied"`. UI explains how to re-enable in site settings (cannot un-deny programmatically).
- **off** — supported, not subscribed. Toggle → `enable()`.
- **on** — subscribed and registered. Toggle → `disable()`.
- **pending** — transition in flight; control disabled.

`enable()`: `Notification.requestPermission()` → if granted, `navigator.serviceWorker.ready` → `GET /notifications/push/webpush-key` → `pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(vapidPublic) })` → `PUT /notifications/push/device { transport: "webpush", token: JSON.stringify(subscription.toJSON()) }`. The api client supplies device + profile headers (satisfies the route's RequireProfile). Cache the VAPID public key (Cache Storage / IndexedDB) so the SW can re-subscribe on `pushsubscriptionchange` without the app open. Success → `on`, toast.

`disable()`: `pushManager.getSubscription()` → `unsubscribe()` (best-effort) → `DELETE /notifications/push/device` → `off`, toast.

Receiving (SW): payload `{id,title,body,link,category}` → `showNotification(title, { body, data:{link}, tag:"n"+id })`. The `tag` is an on-device display-dedup keyed on the notification id (a redelivery of the same notification replaces the visible banner rather than stacking); it is independent of the server's Web Push `Topic` header (which dedups in the push-service queue). `notificationclick` → focus matching-origin client + postMessage `{type:"notification-click", link}`, else `openWindow(link ?? "/notifications")`.

Idempotency / healing: `enable()` is safe to re-run (server upserts); a stale local subscription with no server row heals on re-enable. Permission-granted-but-subscribe-fails → toast, stay `off`.

## Admin config page

`PushSettings.tsx`, registered in `AdminSettingsLayout` `SETTINGS_GROUPS` (Connections group). Pattern copied from `IntegrationsSettings.tsx` (`SettingField` + `CredentialStatus` + `useUpdateServerSetting`). Cards:

- **Web Push** (leads — usable today): `push.webpush.vapid_public`, `push.webpush.vapid_private`, `push.webpush.subject`. "Generate keys" button → `POST /admin/push/generate-vapid-keys` → fills public/private fields (not auto-saved; admin reviews then saves each). Subject defaults to a `mailto:` hint.
- **APNs**: `push.apns.p8_key` (textarea), `push.apns.key_id`, `push.apns.team_id`, `push.apns.bundle_id`.
- **FCM**: `push.fcm.service_account_json` (textarea).

Readiness: card header badge from `GET /admin/push/status`; per-field "configured, leave blank to keep" from the sensitive-status hook (secrets never returned). Each save = `PUT /admin/settings/{key}` `{value}` via `useUpdateServerSetting`; encryption automatic; badges refetch after save.

VAPID keygen endpoint: generates and returns `{vapid_public, vapid_private}`; does not persist. Handler test: non-empty pair, admin-gated.

### Open decision — "Send test push" button

OPTION (admin's call at spec review): a "Send test push" button that creates a one-off system notification addressed to the admin themselves to verify config→subscribe→receive end to end from the UI. Requires one more net-new server endpoint (`POST /api/v1/admin/push/test` → `notifications.Create` a system notification for the caller). Recommended for operability (turns "did I configure this right?" into one click) but it is the only other net-new server surface. **Decision: include it** unless cut at review. (Implementation plan will gate this behind the decision.)

## Error handling

- Capability gating (unsupported, denied) are UI states, not errors; iOS Safari shows "Add to Home Screen to enable push."
- Transient subscribe/register failures → toast, leave `off`; nothing half-committed (server PUT is source of truth, idempotent).
- SW resilience: tolerate partial payload (generic title fallback); `notificationclick` falls back to `/notifications`; failed `pushsubscriptionchange` re-register logs and retries on next app load (browser stops receiving until reopened — never a crash).
- Admin page: save failures toast; readiness badges always reflect server truth (refetch after save); partial config shows "Not configured" until complete; generate-keys failure leaves fields untouched.

## Testing

- `usePushDevice` — unit tests with mocked serviceWorker/PushManager/Notification/api client: status derivation, enable() happy path (permission→key→subscribe→PUT body), permission-denied short-circuit, disable() (unsubscribe + DELETE), idempotent re-enable.
- Settings UI — component tests: each status renders the right control; device-list rows + per-device toggle mutation.
- Service worker — vitest over the pure handlers: payload→showNotification-args mapping, click→target-URL logic. DOM wiring exercised in the manual smoke.
- Admin page — component tests: three cards render, badges reflect `/admin/push/status`, save calls `PUT /admin/settings/{key}` with the right key, generate-keys fills fields.
- Server — VAPID keygen handler test (non-empty pair, admin-gated); test-push endpoint handler test (if included).
- Manual smoke (documented in plan): real browser — configure VAPID, enable push, trigger a notification, confirm OS banner with app closed, click → deep link.

## Implementation sequencing

1. Server: VAPID keygen endpoint (+ test-push endpoint if kept).
2. Web: service worker + registration + VAPID-key caching.
3. Web: `usePushDevice` hook + tests.
4. Web: NotificationsSettings push control + device list.
5. Web: admin PushSettings page + nav.
6. Verification + manual browser smoke.

## Future work

- Mobile clients (APNs/FCM) against the frozen API.
- Shared-device token dedup + active-profile child-suppression (recorded in the push server spec) — informs whether multiple browsers/profiles dedupe.
- Per-category push targeting if category mutes prove too coarse.
- PWA/offline (vite-plugin-pwa) if the app later wants installable offline behavior.
