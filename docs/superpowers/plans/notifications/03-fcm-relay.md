# Privacy-Preserving FCM Relay Spec

**Date:** 2026-04-28
**Status:** Draft
**Scope:** Silo server push integration for Android, opt-in central FCM relay, custom FCM provider configuration
**Depends On:**
- [`00-architecture-overview.md`](./00-architecture-overview.md)
- [`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md)
- [`02-apns-relay.md`](./02-apns-relay.md) — sibling spec; structurally identical with FCM-specific differences

## Summary

Silo should support remote Android notifications through either an opt-in central FCM relay or admin-configured Firebase Cloud Messaging credentials, while keeping notification metadata on the user's own Silo server.

The hosted relay should be a constrained transport service. It should receive only the minimum fields needed to submit a Firebase Cloud Messaging v1 API request:

- an FCM registration token
- the target Firebase project ID and Android package name (allowlisted)
- a push mode
- an opaque server/device correlation key
- an opaque delivery identifier
- optional badge count (disabled by default)

Neither provider path may send titles, item names, usernames, profile names, library names, server URLs, artwork URLs, watched-state metadata, or notification body text through FCM.

When the Android device wakes, the app should fetch real notification metadata directly from the user's Silo server using the existing authenticated server connection.

## Why this spec mirrors APNs

The reasoning, threat model, and fanout flow are identical to the APNs case. Apple and Google both gatekeep mobile push at the app-bundle level: only the developer who signed and published the app can authenticate pushes to it. The same arguments for a hosted relay apply unchanged. The differences are entirely about FCM's protocol and credentials:

- **Auth:** FCM v1 API uses **OAuth2 with a Google service account**, not a JWT signed with a `.p8` key.
- **Endpoint:** Pushes go to `https://fcm.googleapis.com/v1/projects/{project_id}/messages:send`.
- **Project model:** A Firebase project owns the Android app's package name + SHA-1 cert fingerprint mapping. The relay holds one service account JSON per official Firebase project.
- **Topics vs tokens:** FCM supports both topic broadcasts and per-token sends. Silo uses **per-token sends only** — topic broadcasts would leak audience information.
- **Message types:** FCM has `notification` and `data` messages. The privacy design uses **data-only messages** so the OS can't render content the relay shouldn't have constructed.

## Decision

Add configurable Android push providers for the official Silo Android app:

- `off`: no remote Android push
- `silo_relay`: Silo's hosted FCM relay
- `custom_fcm`: admin-supplied FCM service account JSON used directly by the server

The relay submits FCM v1 requests using Silo-controlled Firebase credentials for the official app package. A self-hosted server admin may opt in to use this relay. The relay's API should intentionally prevent notification content from being sent through the relay.

If `custom_fcm` is selected, the user's Silo server calls FCM v1 directly using admin-provided service account JSON. This avoids the Silo relay entirely, but it must use the same minimal payload and metadata-fetch rules as the hosted relay path.

The default push mode should be **Private Data Wake**:

- FCM delivers a data-only message.
- The data payload contains only opaque identifiers.
- The Android app handles the message via `FirebaseMessagingService.onMessageReceived` and constructs a generic local notification while it fetches the actual notification row from the user's server.

The implementation should also support **Background Wake** for low-noise sync, with the explicit understanding that Android background data messages are subject to Doze, App Standby, and battery-optimization restrictions and are not a reliable user-visible notification mechanism.

## Goals

- Make Android push possible without every admin managing their own Firebase project.
- Keep actual notification metadata on the user's Silo server.
- Make relay participation explicit and disabled by default.
- Make FCM credentials configurable for admins who want to avoid the hosted relay.
- Keep the relay stateless for device subscriptions where possible.
- Keep FCM payloads opaque and generic (data-only).
- Keep the hosted relay and custom FCM paths behaviorally equivalent from the app's point of view.
- Fit on top of the durable notification inbox design.

## Non-Goals

- Replacing FCM for Android remote push.
- Shipping notification content through the central relay.
- Requiring Apple push services for this Android path.
- Supporting Huawei Mobile Services (HMS) push, Amazon Device Messaging, or other Android-adjacent push systems in v1. Could be added later as parallel providers.
- Adding marketing, analytics, or delivery-tracking exports to the relay.
- Hiding FCM itself from Android devices. Custom FCM still uses Google FCM.
- Making silent background delivery as reliable as visible notifications.

## Threat Model

### What The User's Server Knows

The user's Silo server knows:

- the notification content
- the profile and user recipients
- the registered local devices
- FCM registration tokens for those devices
- push delivery attempts and failures

This is acceptable because the server is already the authority for the user's media, profiles, sessions, and notification inbox.

### What The Central Relay May Know

The relay may see:

- an opt-in relay account or install identifier
- the server's egress IP address (inherent to any hosted relay — the server connects to it directly; see the identical note in [`02-apns-relay.md`](./02-apns-relay.md))
- FCM registration tokens submitted in send requests
- request timestamps
- coarse push mode, such as `private_data` or `background_wake`
- opaque `collapse_key` values (per-server-keyed HMACs of series identity; same derivation rule as APNs `collapse_id`)
- FCM response status and FCM message names
- opaque delivery identifiers

The relay must not receive enough information to know what media item, notification type, profile, user, library, server hostname, or server URL caused the push.

### What Google May Know

Google / FCM may see:

- the official app package name and Firebase project
- the target FCM registration token
- FCM v1 request headers
- request timing
- the generic FCM data payload (which contains only opaque identifiers)

Google must not receive media titles, usernames, library names, server URLs, artwork URLs, or other notification metadata in the payload. In particular, the design uses **data-only messages**, never `notification` messages, so Google's servers don't carry rendering content.

### Residual Metadata Leakage

This design cannot hide that a device received a Silo push at a particular time, nor the egress IP of the server sending relay requests. It can only hide the meaning and content of the push. Additionally, on Android, FCM delivery may be delayed by Doze / App Standby; an adversary observing wake patterns may infer rough activity but not content.

## User And Admin Controls

### Server Admin Control

The server admin must explicitly choose an Android push provider.

Suggested setting:

```text
notifications.android_push.provider = off | silo_relay | custom_fcm
```

Default:

```text
off
```

If `silo_relay` is selected, the admin configures a relay API key and accepts a clear privacy notice:

```text
Silo Relay can wake Android devices through Firebase Cloud Messaging,
but notification details stay on this server. The relay receives FCM
registration tokens, timestamps, opaque delivery IDs, and generic push mode
only.
```

If `custom_fcm` is selected, the admin configures their own Firebase service account JSON:

```text
notifications.android_push.custom.service_account_json
notifications.android_push.custom.project_id
notifications.android_push.custom.allowed_packages = [...]
```

The server then sends directly to FCM and does not call the Silo relay. The same private payload rules still apply.

### Device User Control

Each Android device must opt in independently:

- the OS notification permission must be granted (Android 13+ requires `POST_NOTIFICATIONS` runtime permission)
- the app must be signed into a Silo server
- the active profile must enable push notifications for that device
- the server admin must have enabled an Android push provider

Admins must not be able to silently enroll every profile/device into remote push without the device having granted OS notification permission.

### Profile Control

Push preferences sit on top of the durable notification preferences from the inbox design. The push-mode field on a `push_devices` row is shared across platforms (`apple` and `android`) so the same `push_mode` enum applies.

Suggested profile/device modes:

- `off`: never send remote push to this device
- `in_app_only`: websocket and inbox only
- `private_push`: generic FCM data wake, fetch details from server
- `full_preview`: out of scope for v1

V1 should ship only `off`, `in_app_only`, and `private_push`.

## System Architecture

```mermaid
sequenceDiagram
  participant App as Android App
  participant Server as User Silo Server
  participant Relay as Silo FCM Relay
  participant FCM as Google FCM

  App->>App: Request POST_NOTIFICATIONS permission (API 33+)
  App->>App: Get FCM registration token via FirebaseMessaging.getToken()
  App->>Server: Register registration token and opaque server_device_id
  Server->>Server: Store encrypted token and device preferences
  Server->>Server: Create durable notification_delivery
  Server->>Relay: Send minimal FCM relay request
  Relay->>FCM: Submit data-only message via FCM v1 API
  FCM->>App: Deliver data message; FirebaseMessagingService.onMessageReceived
  App->>Server: Fetch notification metadata directly
  Server->>App: Return inbox rows for active profile
```

## Ownership Boundaries

### This Repository

Silo owns:

- admin settings for selecting an Android push provider
- profile and device notification preferences
- FCM token registration from Android clients
- durable storage of device registrations
- push fanout from `notification_deliveries`
- relay client integration
- retry, backoff, and FCM error handling
- inbox sync APIs used after wake

### Android Client Repository

The Android app owns:

- FCM registration via Firebase SDK
- notification permission UX (`POST_NOTIFICATIONS` on API 33+)
- device registration API calls
- local server/device correlation
- `FirebaseMessagingService.onMessageReceived` handling
- fetching metadata from the user's server
- rendering local notifications via `NotificationManager` after the wake

### Central Relay Service

The central relay owns:

- FCM service account credentials for the official Silo Firebase project(s)
- relay API authentication
- request validation and rate limiting
- FCM v1 request submission with OAuth2 access token caching
- redacted operational logs
- minimal delivery status reporting

The relay should live outside this repo unless the project later decides to co-locate the service code. This repo defines the contract and implements the self-hosted server side. The relay implementation can share most code with the APNs relay; only the upstream provider client differs.

### Custom FCM Mode

When `custom_fcm` is selected, there is no central relay in the send path.

The user's Silo server owns:

- FCM v1 OAuth2 token generation from the configured service account JSON
- access token caching (Google's OAuth2 access tokens are short-lived; cache and refresh on expiry)
- package allowlisting for the official Silo app package(s) it intends to support
- FCM v1 request construction (data-only)
- FCM response handling
- FCM credential rotation and secret storage

Custom FCM mode must not unlock richer payloads. It exists to let admins avoid the Silo relay, not to bypass the privacy-preserving notification contract.

## Data Model

The `push_devices` and `push_delivery_attempts` schemas are owned by [`02-apns-relay.md`](./02-apns-relay.md) and are platform-tagged from the start (`platform = 'apple' | 'android'`) so this spec adds no new columns. The Android-relevant columns on `push_devices` are:

- `platform text not null` — must be `'android'` for FCM rows.
- `provider text not null` — `'silo_relay'` or `'custom_fcm'`.
- `fcm_token_ciphertext bytea` — required for Android rows.
- `fcm_token_hash text` — required for Android rows; used for dedupe/diagnostics without logging raw tokens.
- `fcm_project_id text` — the Firebase project the token was minted under.
- `fcm_package_name text` — the Android package name (e.g., `com.continuum.app.android`).
- All `apns_*` columns must be null for Android rows. The `CHECK` constraint defined in `02-apns-relay.md` enforces this exclusivity.

Unique constraints (defined in `02-apns-relay.md`): `(profile_id, device_id, platform)` and `(server_device_id)`. Both are intentionally platform-scoped, not provider-scoped — the same Android device cannot register twice even if the admin flips between `silo_relay` and `custom_fcm`.

`push_delivery_attempts` columns relevant to FCM (also owned by 02):

- `provider text not null` — for FCM rows: `'silo_relay'` or `'custom_fcm'`.
- `apns_id text` — null for FCM rows.
- `fcm_message_name text` — populated on success with FCM v1's response `name` (e.g., `"projects/continuum-prod-android/messages/0:..."`); null for APNs rows.
- `attempt_number integer not null` — 1-based; participates in the unique constraint and in the relay `Idempotency-Key`.

Retention is identical to APNs: ~14 days for successes, 30-90 for failures.

## Server API Surface

### Register Android Push Device

```http
POST /api/v1/devices/push/fcm
```

Profile-scoped. Requires normal auth plus active profile context.

Request:

```json
{
  "device_id": "android-device-local-id",
  "fcm_token": "long-fcm-registration-token",
  "fcm_project_id": "continuum-prod-android",
  "fcm_package_name": "com.continuum.app.android",
  "push_mode": "private_push"
}
```

Response:

```json
{
  "id": "01J...",
  "server_device_id": "01JOPAQUE...",
  "enabled": true,
  "push_mode": "private_push"
}
```

Rules:

- `fcm_package_name` must be allowlisted for the configured provider's allowed_packages list.
- `fcm_project_id` must match the configured Firebase project for the active provider.
- registration is idempotent by `(profile_id, device_id, platform)` where platform is implied as `android`.
- token rotation updates the encrypted token and token hash. FCM tokens rotate periodically; the app should call this endpoint whenever `FirebaseMessagingService.onNewToken` fires.
- a disabled server push provider should still allow registration, but should report that remote push is unavailable in the capability response.

### Disable Push Device

Shared with APNs, see [`02-apns-relay.md`](./02-apns-relay.md):

```http
DELETE /api/v1/devices/push/{id}
```

### Push Capability

Extended from the APNs spec to include Android:

```http
GET /api/v1/notifications/capability
```

Response:

```json
{
  "in_app": { "enabled": true },
  "apple_push": {
    "available": true,
    "provider": "silo_relay",
    "available_providers": ["silo_relay", "custom_apns"],
    "supported_modes": ["private_push", "in_app_only"]
  },
  "android_push": {
    "available": true,
    "provider": "silo_relay",
    "available_providers": ["silo_relay", "custom_fcm"],
    "supported_modes": ["private_push", "in_app_only"]
  },
  "webhooks": {
    "available": true,
    "max_per_profile": 10,
    "supported_types": ["discord", "generic"]
  }
}
```

## Relay API Contract

The relay API should be intentionally narrow. The user server should not send an arbitrary FCM payload.

### Send Android Push

```http
POST /v1/fcm/send
Authorization: Bearer <relay_api_key>
Idempotency-Key: <notification_delivery_id>:<push_device_id>:<attempt_number>
```

Request:

```json
{
  "token": "fcm-registration-token",
  "project_id": "continuum-prod-android",
  "package_name": "com.continuum.app.android",
  "mode": "private_data",
  "server_device_id": "01JOPAQUE...",
  "delivery_id": "01JDELIVERY...",
  "badge": null,
  "collapse_key": "01JOPAQUE_COLLAPSE"
}
```

Response:

```json
{
  "request_id": "01JRELAY...",
  "fcm_message_name": "projects/continuum-prod-android/messages/0:1234567890123456%abcdef",
  "status": "accepted"
}
```

Validation:

- `token` is required and must be plausible for FCM (FCM tokens are typically 152-180+ chars, base64url-ish).
- `project_id` must match the configured Firebase project for this relay account's allowed packages.
- `package_name` must be allowlisted for the relay account.
- `mode` must be `private_data` or `background_wake`. Note: this is the **wire mode** (FCM-side); the profile-level mode is `private_push`. See [`00-architecture-overview.md`](./00-architecture-overview.md) "Mode terminology" for the mapping.
- `server_device_id`, `delivery_id`, and `collapse_key` must be opaque values with length limits.
- `collapse_key` derivation (server-side rule, not relay-enforced): identical to the APNs `collapse_id` rule in [`02-apns-relay.md`](./02-apns-relay.md) — `base32(HMAC-SHA256(server_collapse_secret, series_id))` truncated, using the same per-server secret. Never send raw or plainly-hashed `series_id`.
- `badge` is optional. Default should be omitted to avoid leaking unread counts unless the user/admin explicitly enables badge sync.
- no free-form notification title or body fields are accepted.
- no image URL, media ID, username, server hostname, or server URL field is accepted.

### Relay-Built FCM Payloads

For `private_data`, the relay constructs:

```json
{
  "message": {
    "token": "fcm-registration-token",
    "data": {
      "v": "1",
      "wake": "notifications.changed",
      "server_device_id": "01JOPAQUE...",
      "delivery_id": "01JDELIVERY..."
    },
    "android": {
      "priority": "high",
      "collapse_key": "01JOPAQUE_COLLAPSE"
    }
  }
}
```

For `background_wake`, the relay constructs:

```json
{
  "message": {
    "token": "fcm-registration-token",
    "data": {
      "v": "1",
      "wake": "notifications.changed",
      "server_device_id": "01JOPAQUE...",
      "delivery_id": "01JDELIVERY..."
    },
    "android": {
      "priority": "normal",
      "collapse_key": "01JOPAQUE_COLLAPSE"
    }
  }
}
```

Notes:

- The relay sends **data-only messages** (no top-level `notification` field) so Google never carries rendering content. The Android app constructs the local notification in `onMessageReceived`.
- `priority: high` is used for `private_data` so the message wakes the app even under Doze. High-priority data messages are subject to FCM's anti-abuse quotas — if a server hits the quota, deliveries fall back to normal priority and may be deferred.
- `collapse_key` allows multiple pushes for the same series to coalesce when the device is offline, mirroring `apns-collapse-id`.
- The relay does not allow callers to override this payload shape in v1.

## Custom FCM Contract

`custom_fcm` should share the same internal send model as `silo_relay`, but replace the relay HTTP call with a direct FCM v1 API request.

The server-side push sender should accept only a structured internal request (same shape as the relay request body above):

```json
{
  "token": "fcm-registration-token",
  "project_id": "continuum-prod-android",
  "package_name": "com.continuum.app.android",
  "mode": "private_data",
  "server_device_id": "01JOPAQUE...",
  "delivery_id": "01JDELIVERY...",
  "badge": null,
  "collapse_key": "01JOPAQUE_COLLAPSE"
}
```

The sender builds the FCM v1 payload locally using the exact same payload shapes defined above for `private_data` and `background_wake`.

Configuration:

- `service_account_json`: full Firebase service account JSON, stored as a secret.
- `project_id`: Firebase project ID.
- `allowed_packages`: explicit Android package names this server is allowed to send for.

Rules:

- The admin may configure credentials, project ID, and allowed packages.
- The server must not expose a free-form FCM JSON payload setting.
- The server must not expose custom title/body templates for remote push in v1.
- Package values must match the device registration package and the configured allowlist.
- OAuth2 access tokens should be cached and regenerated before expiry (Google access tokens are typically valid for 1 hour).
- Credential validation should provide a test-send or dry-run diagnostic that does not include notification content. FCM v1 supports `validate_only: true` requests for this purpose.

## Wake And Metadata Fetch

When the app receives an FCM data message:

1. `FirebaseMessagingService.onMessageReceived(remoteMessage)` fires.
2. Read `data.v`, `data.server_device_id`, and `data.delivery_id`.
3. Find the local server account that owns `server_device_id`.
4. If the app is in the foreground, update in-app inbox state directly.
5. If background:
   - Construct a generic local notification using `NotificationManager` with a localized "New Silo notification" title.
   - Schedule a `WorkManager` job to fetch the actual metadata from the user's server.
   - When the fetch returns, update the visible notification with real content (or replace it).
6. If the app is opened from the notification, call the server before rendering the target screen.

Suggested fetch:

```http
GET /api/v1/notifications/sync?since=<last_cursor>
```

or:

```http
GET /api/v1/notifications/{delivery_id}
```

The sync endpoint is preferred because push delivery is not guaranteed and multiple notification deliveries can be coalesced.

If the app cannot reach the server, it should keep the generic notification and retry the inbox sync when connectivity returns.

## Fanout Rules

Push fanout should happen after durable inbox delivery commits. Same flow as APNs:

1. `notification_deliveries` row is created. In the same transaction, the fanout worker enqueues one `pending` `push_delivery_attempts` row per enabled Android `push_device` (the dispatch outbox — see [`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md)).
2. The realtime websocket event is published for connected clients.
3. The push dispatcher claims the `pending` attempt rows post-commit, partitioned by platform; a recovery sweeper claims rows older than ~60s whose dispatch never ran.
4. Each claimed attempt sends through the configured Android provider:
   - `silo_relay`: send the minimal relay request.
   - `custom_fcm`: build the same minimal FCM v1 payload locally and send directly.
5. FCM errors update device state and attempt status.

Do not block notification delivery creation on FCM or relay availability.

Relay pacing is shared with APNs: one token bucket (`notifications.push.relay_send_rate`, default 5 req/sec) covers both dispatchers because both consume the same relay account quota. See [`02-apns-relay.md`](./02-apns-relay.md) "Relay pacing".

## FCM Error Handling

The server should handle relay/FCM failures without exposing notification content.

FCM v1 error codes worth handling explicitly:

- `UNREGISTERED` (404): the token is no longer valid (app uninstalled or data cleared). Disable the device and require re-registration.
- `INVALID_ARGUMENT` (400): malformed request. Mark failed and surface admin diagnostics. Should be rare given the constrained payload.
- `SENDER_ID_MISMATCH` (403): the token was minted under a different Firebase project. Disable and log.
- `QUOTA_EXCEEDED` (429): retry with backoff, **honoring the `Retry-After` response header** if present (FCM uses standard HTTP semantics here). Common when high-priority data-message quotas are hit; FCM specifically throttles high-priority data messages that don't surface a user-visible notification on the device. Consider falling back to `priority: normal` after repeated 429s within a cooldown window.
- `UNAVAILABLE` (503): FCM is temporarily down. Retry with backoff.
- `INTERNAL` (500): retry with backoff.
- `THIRD_PARTY_AUTH_ERROR` (401): credential failure (relay or custom_fcm). Surface in admin diagnostics; do not retry until the admin fixes it.
- relay unavailable, FCM unavailable, or network timeout: retry with backoff.

Errors should be visible to admins as operational status, not to normal users unless their device needs reauthorization.

## Privacy Requirements

The implementation must satisfy these requirements before shipping:

- Relay requests contain no notification title or body.
- Relay requests contain no media identifiers.
- Relay requests contain no server hostname or base URL.
- Relay requests contain no profile, username, library, collection, or item names.
- Direct FCM payloads contain no notification title or body. Specifically: **no top-level `notification` object** in the FCM v1 message — data-only messages only.
- Direct FCM payloads contain no media identifiers, server hostname, server URL, profile name, username, library name, collection name, or item name.
- Relay logs redact raw FCM tokens.
- Relay logs redact authorization headers and OAuth2 access tokens.
- Relay logs do not persist full request payloads.
- Server logs redact FCM tokens, custom service account JSON contents, and generated OAuth2 access tokens.
- Badge count is disabled by default.
- Push registration and remote push provider use are opt-in.
- Device unregister disables further push attempts for that device.

## Security Requirements

- Relay API uses TLS only.
- Relay API keys are scoped to a relay account or install identifier.
- Relay API keys can be revoked without changing the user's Silo server authentication.
- Relay requests are rate limited by API key and coarse token hash.
- Relay supports idempotency keys to prevent duplicate FCM sends during retry.
- Relay does not accept arbitrary FCM payload JSON in v1.
- Server stores relay API keys as secrets.
- Server stores custom FCM service account JSON as a secret.
- Server never logs custom FCM service account JSON contents, generated OAuth2 access tokens, or FCM authorization headers.
- Server stores FCM registration tokens encrypted at rest where local secret storage exists.
- Admin diagnostics must not print raw FCM tokens or service account JSON.
- Custom FCM service account JSON must validate as a real Google service account JSON before being accepted (parse + sanity check `type: "service_account"` and required key fields).

## Settings And UX

Admin settings should explain the tradeoff plainly:

```text
Android Push Provider

Off
  No Android remote push. Devices still receive in-app realtime updates while open.

Silo Relay
  Uses Silo's FCM relay to wake Android devices. Notification details stay on
  this server. The relay receives FCM tokens, timestamps, and opaque delivery IDs.

Custom FCM
  Advanced. Send directly to Google FCM using your own Firebase service account.
  Notification details still stay on this server. Only useful if you've published
  your own signed Android app variant under your own Firebase project.
```

Device settings (shared with APNs) should describe the user-visible mode:

```text
Private Push
  Show a generic Silo notification, then fetch details from your server when
  this device wakes.
```

Do not claim the central relay is fully self-hosted. The truthful claim is:

```text
Notification content stays on your server. Google FCM, and the Silo relay
if selected, may still process generic wake messages needed for Android push
delivery.
```

## Implementation Plan

### Task 1: Add Android Push Provider Settings

Files likely involved:

- `internal/api/handlers/settings.go`
- `web/src/lib/settingsManifest.ts` (or admin-scope equivalent)
- settings UI files as needed

Add server/admin settings for:

- provider selection
- relay endpoint
- relay API key
- custom FCM service account JSON
- custom FCM project ID
- custom FCM allowed packages
- badge sync enabled or disabled (shared with Apple settings)

Default provider must be `off`.

### Task 2: Extend Push Device Registration

Files likely involved:

- migration extending `push_devices` from the APNs spec to add FCM-specific columns and a check constraint
- `internal/notifications` package
- `internal/api/handlers/notifications.go`
- `internal/api/router.go`

Add profile-scoped Android push device registration, token rotation, and disable APIs.

If the APNs spec lands first, this task is purely additive (new columns nullable, new endpoint, expanded `platform` enum).

### Task 3: Add Android Push Provider Clients

Files likely involved:

- `internal/notifications/fcm_relay.go`
- `internal/notifications/fcm_direct.go`
- config/settings accessors

Implement the narrow `/v1/fcm/send` relay client. The client should not accept a free-form notification payload.

Implement the direct FCM v1 client for `custom_fcm` using:

- `golang.org/x/oauth2/google` for service-account-based OAuth2 token generation, or an equivalent that doesn't add a heavy Google SDK dependency.
- Cached access token with refresh-before-expiry.
- Standard `net/http` for the FCM v1 endpoint.

Both clients use the same internal request shape and payload builder so the dispatcher code is uniform.

### Task 4: Extend Push Fanout Worker

Files likely involved:

- `internal/notifications/push_fanout.go` (shared with APNs)

Trigger Android push fanout for devices with `platform = 'android'`. Use retries and record `push_delivery_attempts` with `fcm_message_name` populated on success.

### Task 5: Add Android Client Registration And Wake Handling

Files live in the Android client repository, not this server repo.

Required client behavior:

- request `POST_NOTIFICATIONS` permission on Android 13+
- get FCM registration token via `FirebaseMessaging.getInstance().getToken()`
- register token with the user's server
- handle `FirebaseMessagingService.onNewToken` for token rotation
- store `server_device_id` locally, scoped to the active server account
- handle `onMessageReceived` for both foreground and background data messages
- fetch notification metadata after wake/open
- unregister or disable on sign-out / profile removal / app data clear

### Task 6: Add Admin Diagnostics

Expose high-level status (extending the APNs diagnostics surface):

- provider enabled/disabled
- number of registered Android devices
- last relay success
- last relay failure code
- custom FCM credential presence and project/package status
- last custom FCM success
- last custom FCM failure code
- token/package/project mismatch warnings

Do not expose raw FCM tokens or service account JSON.

## Validation Plan

Use minimal verification while the work is still design-only. When implemented, verify:

- relay-disabled servers never call the relay
- `custom_fcm` servers never call the relay
- device registration is profile-scoped and idempotent
- FCM tokens are redacted in logs
- relay request bodies contain no titles, body text, item IDs, server URLs, or profile names
- direct FCM payloads contain no `notification` object and no titles, body text, item IDs, server URLs, or profile names
- custom FCM service account JSON is redacted in logs
- generated OAuth2 access tokens are redacted in logs
- creating one `notification_delivery` sends at most one push per enabled device
- FCM token rotation updates the stored token without duplicating devices
- `UNREGISTERED` and `SENDER_ID_MISMATCH` disable the affected device
- `QUOTA_EXCEEDED` triggers backoff
- app wake fetches notification metadata from the user's server
- server offline after push leaves only the generic notification visible

## Open Questions

- Should official Silo Android builds use one Firebase project per build channel (debug / staging / prod), or share?
- Should badge counts (Android notification dots / numbers) be allowed in `private_push`, or deferred?
- Should the relay store hashed FCM token aliases to avoid sending raw tokens on every request? Defer to v2 unless rate-limit pressure justifies it.
- Should the server fall back to `priority: normal` after repeated `QUOTA_EXCEEDED` to keep deliveries flowing at the cost of latency? Recommendation: yes, with a cooldown timer.
- Should we also add a Huawei Mobile Services (HMS) push path for non-Google Android devices? Out of scope v1; could mirror this spec as `04-hms-relay.md` later.
