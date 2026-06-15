# APNs & FCM 2026 Reference — Privacy-Preserving Push Relay

> **Provenance.** This document reflects **2026-current** Apple (APNs) and Google (FCM HTTP v1) developer documentation, plus the current Go ecosystem and OWASP guidance. Every non-obvious factual claim carries an inline citation in the form `(source: URL, date)`. Where a primary source disagreed with secondary/blog material, the primary source wins and the secondary claim is flagged as inaccurate. Where a fact remains uncertain, it is called out explicitly rather than overstated.
>
> **Compile date:** _<!-- PLACEHOLDER: set at compile time -->_
>
> **Commands assume the repository root is the cwd.** No local absolute filesystem paths appear in this document; all repo references are repository-relative.

## Scope

This is the factual foundation for a self-hosted, privacy-preserving push **relay** — a server that accepts notification requests from authenticated tenants and forwards them to Apple Push Notification service (APNs) and Firebase Cloud Messaging (FCM) on their behalf, without the providers (or other tenants) learning more than necessary. It captures the wire-level contracts (auth, endpoints, headers, payloads, errors, throttling), the recommended Go libraries and tuning, and the security/ops practices the spec and implementation plan are written against. It does **not** cover client-side SDK integration except where client behavior (token rotation, notification-permission gating) constrains relay design.

---

## 1. APNs (2026)

### 1.1 Provider authentication — token-based (JWT, ES256)

APNs supports **both** token-based and certificate-based provider authentication over HTTP/2 + TLS in 2026; Apple does **not** deprecate certificate-based auth. Both have dedicated docs and no deprecation notice (source: https://developer.apple.com/documentation/usernotifications/setting-up-a-remote-notification-server, 2026-06-13). The relay should use **token-based** (`.p8`) auth: the signing key does not expire (it can be revoked), one key covers sandbox + production and all/subset of a team's apps, and there is no yearly certificate renewal.

> **Correction vs. common blog claims:** Third-party posts claiming Apple is "deprecating / shutting down certificate-based auth in 2025" are **inaccurate**. The only confirmed 2025 changes are (a) the APNs **server** TLS root-CA rotation (§1.7) and (b) the optional token-key hardening features (§1.2). Neither deprecates provider auth (source: https://developer.apple.com/documentation/usernotifications/setting-up-a-remote-notification-server, 2026-06-13).

**JWT structure.** APNs supports **only the ES256** algorithm (ECDSA over P-256); no other algorithm is accepted (source: https://developer.apple.com/documentation/usernotifications/establishing-a-token-based-connection-to-apns, 2026-06-13).

```jsonc
// JWT header
{
  "alg": "ES256",      // ES256 ONLY
  "kid": "ABC123DEFG"  // 10-char Key ID from the developer account
}
// JWT claims
{
  "iss": "DEF123GHIJ", // 10-char Team ID
  "iat": 1437179036    // issued-at, seconds since Epoch (UTC)
}
```

The JWT is signed with the `.p8` ECDSA P-256 private key using ES256. It is sent on **every** notification request as an HTTP/2 header in Base64URL-encoded JWT form (source: https://developer.apple.com/documentation/usernotifications/establishing-a-token-based-connection-to-apns, 2026-06-13):

```http
authorization: bearer eyAia2lkIjog...MEYCIQDz...
```

**Lifecycle & regeneration cadence (critical):**

| Rule | Value | Source |
|---|---|---|
| Max token age (`iat`) | ≤ 1 hour, else `ExpiredProviderToken` (403) | (source: …/establishing-a-token-based-connection-to-apns, 2026-06-13) |
| Refresh **no more** often than | once every **20 minutes** (per connection) | (same) |
| Refresh **no less** often than | once every **60 minutes** | (same) |
| Over-frequent refresh → | `TooManyProviderTokenUpdates` (429) | (source: …/handling-notification-responses-from-apns, 2026-06-13) |

**Relay pattern:** Generate **one** ES256 JWT, cache it in memory behind a mutex, and reuse it across **all** senders/connections/topics. Regenerate on a ~50-minute timer (well inside the 1-hour window, never < 20 min apart), or eagerly on a `403 ExpiredProviderToken`. Never sign a fresh JWT per request.

**One key, many servers.** A single key/token can be reused across multiple provider servers, multiple connections, and all (or a subset) of a team's apps/topics — confirmed verbatim by Apple: "You can use the same token from multiple provider servers" and "You can use one token to distribute notifications for all or a subset of your company's apps" (source: https://developer.apple.com/documentation/usernotifications/establishing-a-token-based-connection-to-apns, 2026-06-13). **Caveat:** a connection cannot map to multiple teams — "If you manage APNs for multiple developer accounts, open separate connection pools for each of them" (same source). For a multi-tenant relay, this means **per-team connection pools**.

### 1.2 Token-key hardening (Feb 2025, optional)

On **2025-02-17** Apple added optional scoped token keys; the classic unrestricted team key still works, and "you don't have to update your keys unless you want to take advantage of the new capabilities" (source: https://developer.apple.com/news/?id=wy4tb0uo, 2025-02-17). Two new key types:

| Key type | Scope | Limit |
|---|---|---|
| **Team-scoped key** | Sandbox **or** Production only; all topics in the team | max **2 keys per environment** |
| **Topic-specific (topic-based) key** | Bound to specific bundle IDs (least privilege) | max **200 keys for Sandbox and 200 for Production, with up to 400 topics per topic-based key** |

(source: https://developer.apple.com/documentation/usernotifications/establishing-a-token-based-connection-to-apns, 2026-06-13 — verbatim scoped-key limit confirmed). For a multi-tenant relay these are a meaningful isolation lever: issue separately scoped `.p8` keys per environment/app rather than one all-powerful key.

### 1.3 Endpoints & HTTP/2 connection management

| Environment | Host | Ports |
|---|---|---|
| Production | `api.push.apple.com` | 443 or 2197 |
| Development / sandbox | `api.sandbox.push.apple.com` | 443 or 2197 |

(source: https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns, 2026-06-13). Port 2197 is functionally equivalent to 443 and exists so firewalls can allow APNs while blocking other 443 traffic — keep it as a fallback. `api.push.apple.com` (production) and `api.sandbox.push.apple.com` (sandbox) are the current canonical hosts named in Apple's docs. The older `api.development.push.apple.com` is a **still-valid equivalent alias for sandbox**: as of 2026-06-13 both names resolve to the same Akamai CNAME (`api-sand-vs.push-apple.com.akadns.net`) and the same IP set (verified by DNS, e.g. `17.188.168.x`), so they hit the identical sandbox backend. We **standardize on `api.sandbox.push.apple.com`** (it matches the canonical docs and `sideshow/apns2` v0.25.0's `HostDevelopment` constant), but the alias is not dead — see §5.

- **Transport:** HTTP/2 with **TLS 1.2 or later** (TLS 1.3 acceptable). `:method POST`, `:path /3/device/<device_token>` where the token is the hex bytes of the device token (source: …/sending-notification-requests-to-apns, 2026-06-13).
- **Connection reuse:** "Reuse a connection as long as possible. In most cases, you can reuse a connection for many hours to days. If your connection is mostly idle, you may send a HTTP2 PING frame after an hour of inactivity." Reuse reduces bandwidth/CPU; rapid connect/disconnect is treated as DoS-like behavior (source: …/sending-notification-requests-to-apns, 2026-06-13).
- **Stream multiplexing:** "APNs allows multiple concurrent streams for each connection, but don't assume a specific number of streams." Read the server's `SETTINGS_MAX_CONCURRENT_STREAMS`; do not hardcode. The exact number varies by server load and auth type (same source). A single well-tuned connection handles thousands of pushes/sec.
- **GOAWAY handling:** When APNs terminates a connection it sends an HTTP/2 `GOAWAY` frame whose payload includes JSON with a `reason` key. Apply RFC 9113 §6.8: stop opening new streams, finish in-flight streams with ID ≤ `Last-Stream-ID`, open a new connection for the rest; `NO_ERROR` = graceful shutdown, any other code = connection error (source: https://www.rfc-editor.org/rfc/rfc9113#section-6.8, 2022-06). The relay should drop a GOAWAY'd connection from the pool and transparently reconnect. Go's `net/http2` transport does most of this automatically when one `http.Client` is reused.

```mermaid
flowchart LR
  R[Relay sender goroutines] -->|stream 1..N over 1 conn| C{HTTP/2 conn pool}
  C -->|POST /3/device/token| A[api.push.apple.com:443]
  A -. GOAWAY reason JSON .-> C
  C -->|drop + reconnect| C2[new conn]
  C2 -->|in-flight <= Last-Stream-ID finish on old conn| A
```

### 1.4 Request headers

`:method = POST`, `:path = /3/device/<device_token>`. Payload is **uncompressed** JSON.

| Header | Required | Allowed values / format | Notes |
|---|---|---|---|
| `authorization` | yes (token auth) | `bearer <ES256 JWT>` | Ignored on cert connections |
| `apns-topic` | yes | bundle ID, optionally with a type suffix (see §1.5) | `MissingTopic`/`BadTopic` (400) on error |
| `apns-push-type` | required on **watchOS 6+**, strongly recommended elsewhere | `alert`, `background`, `controls`, `fileprovider`, `liveactivity`, `location`, `mdm`, `pushtotalk`, `voip`, `widgets`, `complication` | Must match payload; invalid → `InvalidPushType` (400) |
| `apns-priority` | no (default 10) | `10`, `5`, `1` | See §1.4.1; invalid → `BadPriority` (400) |
| `apns-expiration` | no | UNIX epoch seconds (UTC) | `0` = deliver once, no store; nonzero = store & retry up to **30 days**; invalid → `BadExpirationDate` (400) |
| `apns-collapse-id` | no | ≤ **64 bytes** | Same value merges notifications on-device (latest wins); >64B → `BadCollapseId` (400) |
| `apns-id` | no | canonical UUID (8-4-4-4-12, lowercase hex) | Echoed in response; auto-generated if omitted; invalid → `BadMessageId` (400) |

(source: https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns, 2026-06-13). `apns-push-type` required on watchOS 6+ confirmed (source: https://developer.apple.com/documentation/usernotifications, 2026-06-13).

**`apns-topic` suffixes by push type** (source: …/sending-notification-requests-to-apns, 2026-06-13):

| Push type | `apns-topic` suffix |
|---|---|
| `complication` | `.h.complication` |
| `controls` | `.push-type.controls` |
| `fileprovider` | `.pushkit.fileprovider` |
| `liveactivity` | `.push-type.liveactivity` |
| `location` | `.location-query` |
| `pushtotalk` | `.voip-ptt` |
| `voip` | `.voip` |
| `widgets` | `.push-type.widgets` |
| `alert` / `background` | (none — bare bundle ID) |

#### 1.4.1 `apns-priority` semantics (corrected)

`apns-priority` is optional; if omitted, APNs sets priority to 10. Three values, with these exact semantics (source: https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns, 2026-06-13):

| Value | Meaning |
|---|---|
| `10` | Send the notification **immediately** (use for alert/sound/badge). |
| `5` | Send based on **power considerations** on the user's device. |
| `1` | Prioritize power over all factors and **do not wake the device** (lowest-power). |

- For `apns-push-type=background`, Apple's guidance is explicit and **narrower** than some sources state: "**Always use priority 5. Using priority 10 is an error.**" Apple does **not** list priority `1` as an accepted alternative for background pushes (source: same, 2026-06-13).
- The claim that priority `1` is a "new tier for Live-Activity-style delivery" is **not supported** by Apple's docs. Live Activity updates use `apns-push-type=liveactivity` (typically priority 10 for immediate or 5 for throttled), not a priority-1 mechanism. Priority `1` is simply the lowest-power option (source: same, 2026-06-13).

### 1.5 Payload shapes & size limits

**Size limits** (source: https://developer.apple.com/documentation/usernotifications/generating-a-remote-notification, 2026-06-13):

| Type | Max payload |
|---|---|
| Regular (alert/background/etc.) | **4096 bytes (4 KB)** |
| VoIP (PushKit) | **5120 bytes (5 KB)** |

Oversize → `413 PayloadTooLarge`. Payload must **not** be compressed. Live Activity payloads use the standard 4 KB limit.

**Payload root** = reserved `aps` dictionary + optional custom keys. `aps` keys (source: same, 2026-06-13): `alert`, `badge`, `sound`, `thread-id`, `category`, `content-available`, `mutable-content`, `target-content-id`, `interruption-level`, `relevance-score`, `filter-criteria`.

**Alert (user-facing) example:**

```json
{
  "aps": {
    "alert": { "title": "Title", "subtitle": "Sub", "body": "Body text" },
    "badge": 1,
    "sound": "default",
    "thread-id": "thread-42",
    "interruption-level": "active",
    "relevance-score": 0.8
  },
  "custom-key": "opaque-relay-payload"
}
```

The `alert` sub-dictionary supports `title`/`subtitle`/`body` plus localization keys (`loc-key`/`loc-args`, `title-loc-key`/`title-loc-args`, `subtitle-loc-key`/`subtitle-loc-args`), `launch-image`, and the legacy `action-loc-key` (source: same, 2026-06-13).

`interruption-level` accepts exactly: `passive`, `active` (default), `time-sensitive` (needs Time Sensitive Notifications entitlement), `critical` (needs Critical Alerts entitlement, Apple-approved). Critical sound uses a **dictionary**: `{ "critical": 1, "name": "...", "volume": 0.0–1.0 }` (source: same, 2026-06-13).

**Background (silent) example** — must set `apns-push-type=background`, `apns-priority=5`, and an `aps` containing **only** `content-available: 1` (no `alert`/`badge`/`sound`):

```json
{ "aps": { "content-available": 1 } }
```

(source: https://developer.apple.com/documentation/usernotifications/pushing-background-updates-to-your-app, 2026-06).

**Live Activity** remote pushes use `apns-push-type=liveactivity`, `apns-topic=<bundleID>.push-type.liveactivity`, **token-based auth only**. The `aps` adds: `event` (`start`|`update`|`end`), `content-state` (decodes into the app's `ActivityAttributes.ContentState`), `timestamp`, `stale-date`, `dismissal-date` (UNIX seconds), optional `relevance-score` and `alert`. `attributes-type` + `attributes` are included **only** for `event: start` (push-to-start). For Live Activities, `relevance-score` is an **unconstrained Double** (Smart Stack ordering), not the 0–1 range used by `UNNotificationContent.relevanceScore` (source: https://developer.apple.com/documentation/activitykit/starting-and-updating-live-activities-with-activitykit-push-notifications, 2026-06).

### 1.6 Status codes & reason strings

**HTTP status codes** (source: https://developer.apple.com/documentation/usernotifications/handling-notification-responses-from-apns, 2026-06-13): 200, 400, 403, 404, 405, 410, 413, 429, 500, 503.

For unsuccessful requests the body is a JSON object `{"reason":"<String>"}`; for **410** it additionally includes a `timestamp` (Number, **milliseconds** since Epoch) indicating when the token became invalid for the topic (source: same).

| Status | Reason strings | Meaning / handling |
|---|---|---|
| 200 | — | Success. Echoed `apns-id` header; in **dev**, `apns-unique-id` header (response-only) for Push Notifications Console Delivery Log. |
| 400 | `BadCollapseId`, `BadDeviceToken`, `BadExpirationDate`, `BadMessageId`, `BadPriority`, `BadTopic`, `DeviceTokenNotForTopic`, `DuplicateHeaders`, `IdleTimeout`, `InvalidPushType`, `MissingDeviceToken`, `MissingTopic`, `PayloadEmpty`, `TopicDisallowed` | Bad request. **Do not retry.** `BadDeviceToken`/`DeviceTokenNotForTopic` ⇒ purge token. |
| 403 | `BadCertificate`, `BadCertificateEnvironment`, `ExpiredProviderToken`, `Forbidden`, `InvalidProviderToken`, `MissingProviderToken`, `UnrelatedKeyIdInToken`, `BadEnvironmentKeyIdInToken` | Auth/cert error. `ExpiredProviderToken` ⇒ regenerate JWT & retry once. `Invalid/MissingProviderToken` ⇒ config error (don't blind-retry). `UnrelatedKeyIdInToken` ⇒ open a new connection. |
| 404 | `BadPath` | Invalid `:path`. Don't retry. |
| 405 | `MethodNotAllowed` | Only `POST` allowed. Don't retry. |
| 410 | `Unregistered`, `ExpiredToken` | Token no longer active for topic. **Permanently stop sending** to that token (until app re-registers). Use the `timestamp` (ms): only purge if your registration is older than it. Not a connection error / no disconnection. |
| 413 | `PayloadTooLarge` | Payload exceeds 4 KB (5 KB VoIP). Don't retry. |
| 429 | `TooManyProviderTokenUpdates`, `TooManyRequests` | Back off. `TooManyProviderTokenUpdates` ⇒ slow JWT refresh (≤ once/20 min). `TooManyRequests` ⇒ per-device-token backoff. |
| 500 | `InternalServerError` | Transient. Retry after ~15 min with backoff. |
| 503 | `ServiceUnavailable`, `Shutdown` | Transient. Retry after ~15 min with backoff. |

(source: https://developer.apple.com/documentation/usernotifications/handling-notification-responses-from-apns, 2026-06-13).

**Retry policy summary.** Never retry permanent errors (`BadDeviceToken`, `Unregistered`, `ExpiredToken`, `PayloadTooLarge`, `DeviceTokenNotForTopic`, `Forbidden`); back off on 429; retry 5xx after ~15 min with backoff. 410 is **not** treated as a connection error (source: same, 2026-06-13). Always parse the JSON `reason`, not just the status code.

### 1.7 APNs server TLS root rotation (2025) — trust-store only

APNs migrated its **server** TLS certificate to a new SHA-2 root, **USERTrust RSA Certification Authority** (a Sectigo root): sandbox **2025-01-20**, production **2025-02-24** (source: https://developer.apple.com/news/?id=09za8wzy, 2024-10-17; effective per https://developer.apple.com/news/upcoming-requirements/?id=02242025a, 2025-02-24).

- This affects only how the relay **validates the APNs server TLS chain** — it does **not** require updating the APNs SSL provider certificates Apple issued you, nor app/account certs.
- Standard OS / Go system root pools (macOS/iOS, Linux `ca-certificates`, Go `crypto/x509` system pool) **already trust** USERTrust RSA, so no action is needed unless the relay **pins** or ships a **custom/minimal** trust store — in which case it must add this root or TLS handshakes fail after the cut-over dates.
- No newer APNs cert/infrastructure requirement is published on Apple's Upcoming Requirements page as of June 2026 (source: https://developer.apple.com/news/upcoming-requirements/?id=02242025a, 2025-02-24).

### 1.8 Background-push throttling

Background (`content-available`) pushes are best-effort and throttled. Apple's verbatim guidance: "The number of background notifications allowed by the system depends on current conditions, but don't try to send more than **two or three per hour**." The system may delay, coalesce, or drop them based on battery/network/usage (source: https://developer.apple.com/documentation/usernotifications/pushing-background-updates-to-your-app, 2026-06). The older "throttle disabled when a debugger is attached" behavior is Apple-Forums developer-experience guidance, **not** in the current spec — do not rely on it.

### 1.9 Legacy binary protocol — dead

The legacy binary (TCP, ports 2195/2196) APNs protocol is fully retired; Apple set the migration deadline to the HTTP/2 provider API at **March 31, 2021** (source: https://developer.apple.com/news/?id=c88acm2b, 2020-10-09; earlier announced https://developer.apple.com/news/?id=11042019a, 2019-11-04). **Do not implement it.** There is no binary fallback in 2026.

### 1.10 Broadcast push (Live Activity channels) — situational

Broadcast push for Live Activities (channels) is current (iOS/iPadOS 18+). You `POST /4/broadcasts/apps/<bundleID>` over HTTP/2 + TLS 1.2+. Required headers: `apns-channel-id` (base64), `apns-push-type=liveactivity`, `apns-priority` (10/5/1), `apns-expiration`; optional `apns-request-id` (UUID, echoed back). Channels are managed via a **separate** channel-management API (`/1/apps/<bundleId>/channels`, `/1/apps/<bundleId>/all-channels`); limit 10,000 channels per app per environment (source: https://developer.apple.com/documentation/usernotifications/sending-broadcast-push-notification-requests-to-apns, 2026-06-13).

> **Hostname nuance (correction):** Apple's broadcast **send** code samples use broadcast-specific hosts `api-broadcast.push.apple.com` (prod) / `api-broadcast.sandbox.push.apple.com` (sandbox) — **not** the plain `api.push.apple.com` used for `/3/device`. Channel **management** uses a third host pair `api-manage-broadcast(.sandbox).push.apple.com` on ports 2195/2196. Apple's own connection-section text confusingly lists the plain hosts (a documented inconsistency) (source: same, 2026-06-13). If broadcast is in scope, verify the exact host against Apple's code samples at build time.

---

## 2. FCM HTTP v1 (2026)

### 2.1 Legacy-API shutdown status

Sending with the legacy FCM HTTP and XMPP APIs was **deprecated 2023-06-20**, and **shutdown began 2024-07-22** — verbatim: "Sending messages (including upstream messages) with the FCM legacy APIs for HTTP and XMPP was deprecated on June 20, 2023, and shutdown begins on July 22, 2024" (source: https://firebase.google.com/docs/cloud-messaging/migrate-v1, 2025-05-29). The legacy HTTP endpoint (`fcm.googleapis.com/fcm/send`), XMPP (`fcm-xmpp.googleapis.com`), and legacy "server key" string auth are **gone**. Only **HTTP v1** remains.

The **batch send** endpoint (`fcm.googleapis.com/batch`) was also discontinued, shutdown date **2024-06-20** (source: same, 2025-05-29). For a raw-protocol relay there is **no batch endpoint** — send one `POST` per registration token and fan out via HTTP/2 multiplexing or a bounded concurrency pool.

### 2.2 OAuth2 service-account authentication

Auth is **OAuth2 service-account bearer tokens**. Recommended (least-privilege) scope: `https://www.googleapis.com/auth/firebase.messaging` (the broader `cloud-platform` works but is a superset) (source: https://firebase.google.com/docs/cloud-messaging/auth-server, 2025-05-29). Send the token as:

```http
Authorization: Bearer <oauth2_access_token>
```

**Token minting (RFC 7523 JWT-bearer grant):** build a JWT signed **RS256** with the service-account private key, then exchange it (source: https://developers.google.com/identity/protocols/oauth2/service-account, 2025):

```jsonc
// JWT claims for the token exchange
{
  "iss": "<service-account-email>",
  "scope": "https://www.googleapis.com/auth/firebase.messaging",
  "aud": "https://oauth2.googleapis.com/token",
  "iat": 1700000000,
  "exp": 1700003600  // iat + up to 3600s (max 1 hour)
}
```

```http
POST https://oauth2.googleapis.com/token
Content-Type: application/x-www-form-urlencoded

grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer&assertion=<signed_JWT>
```

Response: `{ "access_token": "...", "token_type": "Bearer", "expires_in": 3600 }`. **Access tokens are valid 3600s (1 hour).** Cache and re-mint before expiry (≈5-minute safety margin); never mint per request (source: same, 2025). Prefer a maintained Google auth library over hand-rolling.

**Credential resolution (ADC):** `GOOGLE_APPLICATION_CREDENTIALS` env var (path to SA JSON) → platform attached/default SA → error. Firebase: the JSON "should be kept private. Do not add it to public repos!" (source: https://firebase.google.com/docs/cloud-messaging/send/v1-api, 2026-06). On GCP, prefer keyless auth (§4.5).

### 2.3 Endpoint & message JSON

```http
POST https://fcm.googleapis.com/v1/projects/{PROJECT_ID}/messages:send
Authorization: Bearer <oauth2_access_token>
Content-Type: application/json
```

The path accepts the project ID **or** project number. Host is global `fcm.googleapis.com`; **no** regional/data-residency send endpoints exist as of June 2026, and no new required top-level fields were introduced through 2025–2026 (source: https://firebase.google.com/docs/cloud-messaging/send/v1-api, 2026-06-12).

**Request body** = `{ "validateOnly"?: bool, "message": Message }`. `message` is **required** and must carry **exactly one** target: `token`, `topic`, or `condition` (source: https://pkg.go.dev/google.golang.org/api/fcm/v1, current). For a relay sending to one device, use `message.token`.

```jsonc
{
  "message": {
    "token": "<registration_token>",
    "data": { "k1": "v1", "k2": "v2" },     // map<string,string> ONLY
    "android": { "priority": "HIGH", "ttl": "0s", "collapseKey": "sync" },
    "apns": { "headers": { "apns-priority": "10" }, "payload": { "aps": { /* ... */ } } }
  }
}
```

**Data-only emphasis (relay-critical):** A message with a `notification` block is **auto-displayed** by the FCM SDK when the app is backgrounded/killed, and the app code does **not** run until the user taps it. A **data-only** message (no `notification` block) is **always** delivered to the app's handler (`onMessageReceived` / background handler) and never auto-displayed — full app control in foreground and background. **A relay that needs its own logic/custom display on every message should send DATA-ONLY messages** (source: https://firebase.google.com/docs/cloud-messaging/customize-messages/set-message-type, 2025-05-29).

**`message.data` is `map<string,string>` — all values MUST be strings** (no nested objects/numbers/booleans; JSON-stringify anything structured). Reserved/forbidden keys: `from`, `message_type`, `notification`, and any key prefixed `google.`/`gcm.` (or reserved words) → `INVALID_ARGUMENT`. `android.data` overrides top-level `data` for Android (source: https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages, 2025-05-29; https://firebase.google.com/docs/cloud-messaging/error-codes, 2026-06-12).

**Payload size:** max **4096 bytes** for token/device sends, **2048 bytes** for topic sends; over → `INVALID_ARGUMENT` (source: https://firebase.google.com/docs/cloud-messaging/error-codes, 2026-06-12).

### 2.4 AndroidConfig

`message.android` (REST camelCase) (source: https://pkg.go.dev/google.golang.org/api/fcm/v1, current):

| Field | Type / values | Notes |
|---|---|---|
| `priority` | `"NORMAL"` \| `"HIGH"` (lowercase also accepted) | Default: NORMAL for data messages, HIGH for notification messages. HIGH attempts immediate delivery and can wake a dozing device. |
| `collapseKey` | string | Group id; only the last queued message delivered on reconnect. **Not** a request-validation limit: FCM *stores* at most **4** distinct collapse keys per device while the device is offline; a 5th evicts one nondeterministically (a delivery/storage behavior — see §2.7). The API does **not** reject a request for using a 5th distinct key. |
| `ttl` | Duration string in seconds with `s` suffix, e.g. `"3s"`, `"3.000000001s"` | `"0s"` = deliver-now-or-drop. Range **0 – 2,419,200s (28 days)**; out-of-range → `INVALID_ARGUMENT`. |
| `restrictedPackageName` | string | Deliver only if the token's app package matches. |
| `directBootOk` | bool | Allow delivery during Direct Boot. |
| `data` | map<string,string> | Overrides `message.data` for Android. |

Newer **optional** fields exist (`bandwidthConstrainedOk`, `restrictedSatelliteOk`); admin-SDK support landed ~March 2026; both are documented Optional and do not change the required contract (source: https://firebase.google.com/docs/cloud-messaging/send/v1-api, 2026-06-12).

### 2.5 `validateOnly` (dry-run)

Set top-level `validateOnly: true` to fully validate the request (token format, payload size, field correctness) and return a message-id-shaped response **without delivering**. Default `false` (source: https://pkg.go.dev/google.golang.org/api/fcm/v1, current). Use in CI/tests.

### 2.6 Error model

Error body shape (source: https://firebase.google.com/docs/cloud-messaging/error-codes, 2026-06-12):

```json
{ "error": { "code": 404, "message": "...", "status": "UNREGISTERED",
  "details": [ { "@type": "type.googleapis.com/google.firebase.fcm.v1.FcmError", "errorCode": "UNREGISTERED" } ] } }
```

Classify on `error.status` / the FcmError `errorCode`, not just the HTTP code.

| HTTP | `error.status` | Meaning | Handling |
|---|---|---|---|
| 400 | `INVALID_ARGUMENT` | Malformed payload **or** bad token (bad format, mismatched package, oversize, reserved data key, bad TTL). `details` = `google.rpc.BadRequest`. | **Do not retry.** Delete token **only** if payload is independently known-valid; otherwise fix payload. |
| 401 | `THIRD_PARTY_AUTH_ERROR` | Invalid/missing APNs cert/key or Web Push auth credential. | Fix credentials (ops alert). **Do not retry.** |
| 403 | `SENDER_ID_MISMATCH` | Authenticated sender ID ≠ sender that registered the token. | Send from the correct project; permanent. **Do not retry.** |
| 404 | `UNREGISTERED` | App unregistered (uninstall, APNs token invalid, 270-day Android inactivity, restore). | **Permanently delete the token.** Never retry. |
| 429 | `QUOTA_EXCEEDED` (RESOURCE_EXHAUSTED) | Project / device / topic rate quota exceeded. `details` = `google.rpc.QuotaFailure` naming which quota. | Retry with exponential backoff; min initial delay **1 minute** for project- and topic-rate cases. |
| 500 | `INTERNAL` | Unknown internal error. | Retry per "Handling retries"; **no** documented `Retry-After`. |
| 503 | `UNAVAILABLE` | Server overloaded / temporarily unavailable. | Retry with backoff + jitter; **honor `Retry-After`** if present. |
| (none) | `UNSPECIFIED_ERROR` | Catch-all; "No more information is available about this error." | Conservative: **do not** auto-delete token; surface for investigation. |

(source: https://firebase.google.com/docs/cloud-messaging/error-codes, 2026-06-12 / 2026-06-13).

> **Corrections / precision notes:**
> - `QUOTA_EXCEEDED` does **not** carry a `google.rpc.RetryInfo` per FCM's own page — only `google.rpc.QuotaFailure` is promised. General Google API guidance allows `RetryInfo`; treat its presence as optional and fall back to `Retry-After`/60 s (source: same, 2026-06-13).
> - `INVALID_ARGUMENT` (400) is **ambiguous**: it can mean malformed payload **or** bad token. Only treat it as a dead token when the payload is independently validated (source: same, 2026-06-12).
> - Strictly, `UNSPECIFIED_ERROR` is the `FcmError.errorCode` inside `details[]`, not the top-level google.rpc `status` — operationally equivalent (source: same, 2026-06-13).

### 2.7 Retry-After, backoff, quotas, throttling

**Retry classification at scale** (source: https://firebase.google.com/docs/cloud-messaging/scale-fcm, 2026-06-12):

- **Never retry** 400 / 401 / 403 / 404 — "abort, and do not retry."
- **429:** "retry after waiting for the duration set in the retry-after header. If no retry-after header is set, default to **60 seconds**."
- **500 / 503:** "retry with exponential back-off" (+ jitter; honor `Retry-After` on 503 if present).
- "Wait at least **10 seconds** before retrying a failed request"; set ≥10 s send timeout; cap retries; a request still failing after **60 minutes** is an outage/miscategorization, not something to retry forever.

**Exponential backoff must use jitter** ("varying the retry delays through a random process"). **Smooth traffic:** ramp 0→max RPS over ≥60 s, and "avoid sending messages within a 2 minute window of each of the :00, :15, :30, and :45 minute marks" to dodge synchronized spikes (source: same, 2026-06-12).

**Project quota.** Default per-project downstream quota is **600,000 messages per minute** ("covers over 99% of FCM developers"), enforced via a **600K-token 1-minute token bucket that refills to full at the end of each (non-clock-aligned) minute**. Over quota → `429 RESOURCE_EXHAUSTED` (`QUOTA_EXCEEDED`) until refill. **The quota measures messages, not requests.** Client errors (HTTP 400–499) count against quota **except 429s** (source: https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas, 2026-06-13; corroborated https://firebase.google.com/docs/cloud-messaging/scale-fcm, 2026-06-12).

> **Correction:** The research claim that "different message types cost different token amounts" is **not** documented and is treated as inaccurate — quota is counted **per message** regardless of type; topics are merely more efficient because one request fans out to many subscribers (source: https://firebase.google.com/docs/cloud-messaging/scale-fcm, 2026-06-12).

**Per-device Android limits:** up to **240 messages/minute** and **5,000 messages/hour** to a single device (headroom for bursts, e.g. rapid chat). On iOS, FCM returns an error when APNs per-device limits are exceeded (source: https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas, 2026-06-13).

> **Correction:** Exceeding the **per-device** Android caps is **not** reported as `QUOTA_EXCEEDED` (429). `429 / QUOTA_EXCEEDED` is the **project-level** per-minute quota. Firebase's throttling/quotas page does **not** specify a particular error code or status string for the per-device caps — it only says "For iOS, we return an error when the rate exceeds APNs limits." Secondary sources name the per-device signal `DeviceMessageRateExceeded`, but this string is **not** firmly documented on a primary Google page, so **classify it defensively** rather than matching an exact literal (source: https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas, 2026-06-13).
>
> **Relay handling (per-device rate).** Because the exact code/string is uncertain, the relay should treat any per-device-rate signal — whether surfaced as an `INVALID_ARGUMENT`-adjacent error, a quota-style `RESOURCE_EXHAUSTED` that names a per-device (not project) quota in `details`, or a `DeviceMessageRateExceeded`-shaped string — as a **transient, retryable rate condition**: map it to a caller-facing **429 with a conservative default `Retry-After`** and **never** as a token purge (the token is healthy; only the rate is too high). Detect on `error.status` / the `FcmError.errorCode` and the `QuotaFailure` scope, not on a hardcoded string. The relay's whole purpose is fanning bursty per-device wakes, so this is a plausible real outcome that must have a defined mapping rather than falling through to `UNSPECIFIED_ERROR`/502.

**Collapsible-message throttling:** burst of **20 per app per device**, refill **1 every 3 minutes** (source: same, 2026-06-13).

> **Correction:** Exceeding the collapsible burst is a **silent delay** (battery protection), **not** a `QUOTA_EXCEEDED` error: "we delay messages to reduce the impact on a user's battery" (source: same, 2026-06-13).

**Distinct collapse keys:** FCM stores at most **4** different collapsible messages per device (one per distinct `collapse_key`); a 5th evicts a key nondeterministically. Default `collapse_key` = app package name (source: https://firebase.google.com/docs/cloud-messaging/customize-messages/collapsible-message-types, 2026-06-12).

> **Design implication (collapse-key cap vs. per-series keys).** If the relay derives `collapse_key` per notification series (e.g. a per-series HMAC), a single device that follows **more than 4 active series** will exceed FCM's 4-distinct-key store, and excess distinct keys evict each other nondeterministically — so **collapsible coalescing is best-effort**, not guaranteed, once a device crosses 4 series. This is acceptable only if a dropped collapsible wake is recoverable (e.g. the caller's durable inbox + sync covers it); otherwise the caller should cap distinct `collapse_key`s per device. **Asymmetry to note:** APNs has **no** documented per-device cap on the number of distinct `apns-collapse-id` values, so this 4-key eviction behavior is FCM-only — the two providers are not symmetric here, and the cross-provider design docs should reconcile this rather than assume parity.

**Non-collapsible offline storage (Android):** up to **100** messages stored while offline; beyond that, **all** stored messages are discarded and the device gets a single "contact your server" signal on reconnect. Do not treat FCM as a durable queue; the app must re-sync (source: same, 2026-06-12).

**Quota increases:** self-serve up to **+25%** in Google Cloud Console (gated on ≥80% usage for ≥5 consecutive minutes/day, <5% client-error ratio at peak, scale best practices). Larger/temporary bumps via Firebase Support: ≥15 days lead (≥30 days for >18M msgs/min), ≤2 events/year, ≤30-day total duration (source: https://firebase.google.com/docs/cloud-messaging/throttling-and-quotas, 2026-06-13).

### 2.8 High-priority data-message behavior & power management

- **Normal priority:** delivered immediately when awake; in Doze, batched to maintenance windows. **High priority:** FCM attempts immediate delivery, can wake a sleeping device, grants temporary network + a partial wakelock (source: https://developer.android.com/training/monitoring-device-state/doze-standby, 2024-07-28).
- **Deprioritization:** A high-priority message that does **not** produce a user-facing notification gets **deprioritized to normal** (and may be throttled): "If FCM detects a pattern in which messages don't result in user-facing notifications, your messages may be deprioritized to normal priority." FCM evaluates ~7 days of per-app-instance behavior (source: https://firebase.blog/posts/2025/04/fcm-on-android/, 2025-04-17). **Do not use high priority for silent data-only syncs; use normal priority and reserve high priority for messages that produce a notification.**
- **App Standby Buckets:** high-priority delivery is still subject to the device's standby-bucket quota; once exhausted, further high-priority messages are effectively treated as normal. Exact per-bucket numeric caps are **not published** by Google and are dynamic/OEM-dependent (source: https://firebase.blog/posts/2019/02/life-of-a-message/, 2019-02-26).
- **Background-restricted apps:** receive **no** FCM messages (any priority, any state) until launched to the foreground (source: https://developer.android.com/training/monitoring-device-state/doze-standby, 2024-07-28).
- **Android 13+ (`POST_NOTIFICATIONS`):** notifications require the runtime `POST_NOTIFICATIONS` permission; without it, delivered notification messages are silently dropped. If the app's first notification channel is created while in the background (which the FCM SDK does on first receipt), Android won't display or prompt until the next app open — so early notifications are **lost**. **Data-message delivery to `onMessageReceived` is unaffected** (source: https://developer.android.com/develop/ui/views/notifications/notification-permission, 2024-08-08).

### 2.9 Registration-token lifecycle

- **Rotation hook:** `onNewToken()` fires on initial generation and every refresh (including FCM-initiated refresh when "the security of the previous token had been compromised"). Tokens also change on reinstall, data-clear, device restore. Persist the token to your server inside `onNewToken` (source: https://firebase.google.com/docs/cloud-messaging/manage-tokens, 2026-06-13).
- **Android inactivity expiry:** "When stale tokens reach **270 days** of inactivity, FCM considers them expired." iOS/web rely on APNs/Web Push (no inactivity-based expiry), so cleanup there is driven by send-time error responses (source: same, 2026-06-13).
- **Freshness strategy:** store each token **with a server timestamp** (FCM SDKs don't provide one), refresh ~**monthly** (no benefit more often than weekly), skip tokens outside your staleness window before sending, and delete on `UNREGISTERED` (404) or on `INVALID_ARGUMENT` (400) **when the payload is verified valid** (source: same, 2026-06-13).

---

## 3. Go ecosystem (2026)

> **Version targeting:** `jackc/pgx v5.10` and `firebase.google.com/go/v4 v4.20` both require **Go 1.25+**, so target **Go 1.25+** for the whole relay (source: https://pkg.go.dev/github.com/jackc/pgx/v5, 2026-06-03; https://pkg.go.dev/firebase.google.com/go/v4/messaging, 2026-05-14).

### 3.1 APNs client

| Choice | Version | Rationale |
|---|---|---|
| `github.com/sideshow/apns2` | **v0.25.0** (tagged 2024-10-25 UTC; pkg.go.dev shows "Oct 24, 2024" due to TZ rounding) | De-facto Go APNs HTTP/2 client. Auto-manages ES256 JWT generation/refresh and HTTP/2 connection reuse. Host constants match Apple. **Hold ONE `*apns2.Client` per process/environment; never recreate per push** (TLS setup is slow). One client "can potentially do 4,000+ pushes per second." (source: https://github.com/sideshow/apns2/blob/v0.25.0/client.go, 2024-10-25) |
| Hand-rolled (alternative) | `net/http` + `golang.org/x/net/http2` (**v0.56.0**, 2026-06-09) + `golang-jwt/jwt/v5` (**v5.3.1**) | Full control of the HTTP/2 transport; mirror apns2: parse `.p8` EC key, sign one ES256 JWT, cache ~50 min, reuse one `*http2.Transport` with `ReadIdleTimeout`/`PingTimeout` set. (source: https://pkg.go.dev/github.com/golang-jwt/jwt/v5, 2026-01-01; https://pkg.go.dev/golang.org/x/net/http2, 2026-06-09) |

`apns2` internals (verified against v0.25.0 source): `HostProduction = "https://api.push.apple.com"`, `HostDevelopment = "https://api.sandbox.push.apple.com"`; `NewTokenClient(*token.Token{AuthKey, KeyID, TeamID})` / `NewClient(cert)`; chain `.Production()`/`.Development()`. `token.TokenTimeout = 3000` (50 min, deliberately under Apple's 1-hour window); `GenerateIfExpired()` regenerates under a mutex. Timeouts: `HTTPClientTimeout=60s`, `ReadIdleTimeout=15s`, `TCPKeepAlive=15s`, `TLSDialTimeout=20s`. `ClientManager` (MaxSize 64, MaxAge 10m) pools clients across multiple certs/keys (source: https://github.com/sideshow/apns2/blob/v0.25.0/client.go, 2024-10-25; token/token.go, same).

> **Maintenance caveat:** `apns2` has had no release since v0.25.0 (Oct 2024); bus-factor risk. Verify recent commit/issue activity before committing vs. the hand-rolled path (see §5, item 2).

### 3.2 FCM / OAuth2

Pick **exactly one** of these two FCM approaches, not both:

| Choice | Version | Rationale |
|---|---|---|
| `golang.org/x/oauth2/google` (lighter) | **v0.36.0** (2026-02-11) | Best for a **fixed/known payload**: `CredentialsFromJSONWithType(ctx, jsonKey, google.ServiceAccount, "https://www.googleapis.com/auth/firebase.messaging")` → `creds.TokenSource` already caches+auto-refreshes; POST your own JSON to the v1 endpoint with a tuned HTTP/2 client. Fewer deps; full control of connection reuse; **you implement retries/error-classification**. Note: `CredentialsFromJSON` is deprecated in favor of `CredentialsFromJSONWithType`. (source: https://pkg.go.dev/golang.org/x/oauth2/google, 2026-02-11) |
| `firebase.google.com/go/v4/messaging` (batteries-included) | **v4.20.0** (2026-05-14, Go 1.25+) | Auto-mints/caches OAuth token, auto-retries transient errors, rich error helpers: `IsUnregistered` (drop token), `IsQuotaExceeded`, `IsUnavailable`, `IsInvalidArgument`, `IsThirdPartyAuthError`, `IsSenderIDMismatch`. Use `Send` / `SendEach` / `SendEachForMulticast` (each = one HTTP call per message, ≤500). **`SendAll`/`SendMulticast` are deprecated (and removed in newer SDK majors)** — do not use. (source: https://pkg.go.dev/firebase.google.com/go/v4/messaging, 2026-05-14) |

For a privacy-preserving relay forwarding opaque tenant payloads, the **`x/oauth2/google` + custom HTTP/2 client** path is usually preferable: it avoids the heavy Admin SDK and gives direct control of connection reuse and error handling.

### 3.3 HTTP/2 tuning (both transports)

`golang.org/x/net/http2.Transport` (**v0.56.0**, 2026-06-09) knobs (source: https://pkg.go.dev/golang.org/x/net/http2, 2026-06-09):

- `ReadIdleTimeout` (~10–15 s): if no frame received for this duration, send a PING health check (0 = disabled).
- `PingTimeout` (~15 s, default): close the conn if no PING response arrives.
- `WriteByteTimeout`: close if no bytes can be written within the timeout.
- `StrictMaxConcurrentStreams` (bool): if `false` (default) new TCP conns open to stay under each conn's `SETTINGS_MAX_CONCURRENT_STREAMS`; if `true` the server limit is global and callers block.

**Setting a non-zero `ReadIdleTimeout` + `PingTimeout` is the single most important tuning for a long-lived relay** — it reaps half-open APNs/FCM connections. Do not hardcode max-concurrent-streams; let the transport read the server SETTINGS (the exact APNs/FCM advertised value is not a published primary-source constant — measure at runtime; see §5, item 3).

**Inbound server:** `net/http` gets HTTP/2 automatically over TLS and supports graceful shutdown. **Set explicit timeouts** — the zero value means no timeout: `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes` (default 1 MiB). On SIGTERM call `srv.Shutdown(ctx)` with a bounded context (source: https://pkg.go.dev/net/http, 2026-06-01).

### 3.4 Storage

| Choice | Version | Rationale |
|---|---|---|
| `github.com/jackc/pgx/v5` + `pgxpool` | **v5.10.0** (2026-06-03, Go 1.25+) | Recommended Postgres driver/pool. Use `pgxpool.New(ctx, dsn)` (not `pgx.Connect`) for a server — concurrency-safe, health-checked. Token store, API-key store, idempotency. (source: https://pkg.go.dev/github.com/jackc/pgx/v5, 2026-06-03) |
| `github.com/redis/go-redis/v9` | **v9.20.1** (2026-06-11) | Context-first Redis client with built-in pooling. Distributed rate-limiting, idempotency in-flight locks, dedup/queue state. (source: https://pkg.go.dev/github.com/redis/go-redis/v9, 2026-06-11) |

### 3.5 Observability

| Choice | Version | Rationale |
|---|---|---|
| `log/slog` (stdlib) | Go 1.21+ | Structured logging, no third-party dep. `slog.NewJSONHandler` for production aggregation; redaction middleware (§4.6). (source: https://pkg.go.dev/log/slog, 2026-06-01) |
| `github.com/prometheus/client_golang` | **v1.23.2** (2025-09-05) | Prometheus instrumentation; custom registry + `promhttp.HandlerFor` for `/metrics`. Instrument per-provider send latency (histogram) and outcome counters labeled by provider + status/reason. (source: https://pkg.go.dev/github.com/prometheus/client_golang/prometheus, 2025-09-05) |

### 3.6 Reference pattern (not a dependency)

`github.com/appleboy/gorush` is a maintained reference push **server** (built on `sideshow/apns2`, supports FCM) with a notification queue, worker pool, and pluggable stats storage — useful as a **pattern reference** for worker pools and retry/queue design, but it is a full server, not a library to embed (source: https://github.com/appleboy/gorush, 2025-11-20).

### 3.7 Pinned versions (summary)

```
github.com/sideshow/apns2            v0.25.0
github.com/golang-jwt/jwt/v5         v5.3.1
golang.org/x/net                     v0.56.0   // http2
golang.org/x/oauth2                  v0.36.0
firebase.google.com/go/v4            v4.20.0   // only if using the Admin SDK path
github.com/prometheus/client_golang  v1.23.2
github.com/jackc/pgx/v5              v5.10.0
github.com/redis/go-redis/v9         v9.20.1
golang.org/x/time                    (rate — see §4.2)
// Go 1.25+ required
```

---

## 4. Security & ops (2026)

### 4.1 API-key storage & comparison

- **Generate** 32 bytes from `crypto/rand`; format as `<prefix>_<base62/base64url>` where the **prefix** (8–12 chars, optionally tenant/env-tagged) is **non-secret**, stored in cleartext, and **indexed**. The remaining 32 bytes are the secret. This GitHub/Stripe-style design lets you `SELECT` the row by indexed prefix in O(1), then constant-time-compare the hash — no full-table scan, no SQL index over the secret (engineering consensus aligned with OWASP API2:2023, source: https://owasp.org/API-Security/editions/2023/en/0xa2-broken-authentication/, 2023).
- **Hashing:** Store **SHA-256** (or **HMAC-SHA-256** with a server-side pepper kept in the secret manager) of the secret — never plaintext. For **high-entropy random keys** (≥128, ideally 256 bits), a fast hash is acceptable; OWASP's slow-KDF warning is scoped to **low-entropy human passwords**, where "Fast hashing algorithms such as SHA-256 are not suitable" — that warning does **not** apply to 256-bit random API keys, and `argon2id` per-request would needlessly throttle the relay (source: https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html, 2026-06). _Note: this passwords-vs-keys distinction is engineering consensus consistent with OWASP, not a single verbatim OWASP line — see §5, item 12._
- **If** you ever store a low-entropy password (not API keys), OWASP's current argon2id minimum is **m=19456 KiB (19 MiB), t=2, p=1** with a unique per-secret salt (source: same, 2026-06). Do **not** apply argon2id to per-request API-key verification.
- **Comparison:** use `crypto/subtle.ConstantTimeCompare(x, y []byte)` on the fixed-length (32-byte) hashes. Never use `==` or `bytes.Equal` on raw secret material (source: https://pkg.go.dev/crypto/subtle, current).
- **OWASP API2:2023:** "API keys should not be used for user authentication. They should only be used for API clients authentication." Never put keys in URLs/query strings; implement anti-brute-force on auth endpoints (source: https://owasp.org/API-Security/editions/2023/en/0xa2-broken-authentication/, 2023).
- **Metadata per key:** `account_id`, `key_hash`, `prefix` (indexed), `created_at`, `last_used_at` (written throttled, e.g. ≤ once/min, to avoid a DB write per request), `expires_at` (optional), `revoked_at`. Support **multiple live keys per account** for zero-downtime rotation; reject revoked/expired keys on every request (aligned with OWASP API2:2023, source: same, 2023).

### 4.2 Rate limiting

- **Single instance:** `golang.org/x/time/rate` token bucket — `NewLimiter(r Limit, b int)`. Use `Allow()` to drop/reject (return 429) on inbound; `Wait(ctx)`/`Reserve()` to pace outbound APNs/FCM calls. `Reservation.Delay()` gives the wait → use it to compute `Retry-After`. Per-account: `map[accountID]*Limiter` with eviction (per-instance only) (source: https://pkg.go.dev/golang.org/x/time/rate, current).
- **Multi-instance (behind LB):** per-instance limiters no longer enforce a global limit — use a **Redis-backed distributed limiter** with an **atomic Lua** script (refill → check → consume in one `EVAL`), state as a Redis HASH `{tokens, last_refill_ts}` + TTL for idle-account eviction. Alternatives: `redis-cell` (GCRA, `CL.THROTTLE` returns remaining quota + retry-after) or `redis-gcra` Lua scripts. Decide **fail-open vs fail-closed** when Redis is down (fail-open for availability; fail-closed for abuse-sensitive endpoints) (source: https://github.com/Losant/redis-gcra, current).
- **Response:** on limit exceeded, return **`429 Too Many Requests`** (RFC 6585 §4, 2012-04) with a **`Retry-After`** header. ABNF: `Retry-After = HTTP-date / delay-seconds`, `delay-seconds = 1*DIGIT` (RFC 9110 §10.2.3, 2022-06). **Common bug to avoid:** do **not** emit an absolute Unix epoch integer — `delay-seconds` is a **relative** count of seconds; an HTTP-date must use the full date format. Prefer an integer relative-seconds value from `Reservation.Delay()` (source: https://www.rfc-editor.org/rfc/rfc9110.html#section-10.2.3, 2022-06).
- **OWASP API4:2023** (renamed "Unrestricted Resource Consumption"): enforce rate limits, "Define and enforce a maximum size of data on all incoming parameters and payloads," cap per-account push volume, and "Configure spending limits for all service providers/API integrations" — directly relevant to capping per-tenant APNs/FCM spend (source: https://owasp.org/API-Security/editions/2023/en/0xa4-unrestricted-resource-consumption/, 2023).

### 4.3 Idempotency

Accept an **`Idempotency-Key`** header (UUIDv4, **scoped per account**). _Status note:_ this is an IETF **draft** (`draft-ietf-httpapi-idempotency-key-header-07`, 2025-10-15), **not** a ratified RFC, and that version **expired 2026-04-18** — treat the header **name** and pattern as the de-facto standard (Stripe uses the same header), but do not cite it as an RFC (source: https://datatracker.ietf.org/doc/html/draft-ietf-httpapi-idempotency-key-header-07, 2025-10-15).

Status-code lifecycle from the draft:

| Situation | Response |
|---|---|
| First request | Process; record the response. |
| Duplicate, original **completed** | Replay the previously completed result (success or error). |
| Duplicate, original **in-flight** | **409 Conflict** (RFC 7807 body, type `.../idempotency`). |
| Same key, **different payload** | **422 Unprocessable Content**. |
| Missing key where required | **400 Bad Request**. |

(source: same, 2025-10-15).

**Stripe-style production model** (de-facto reference): header `Idempotency-Key`, value up to 255 chars, recommend UUIDv4; saved result (status + body) replayed for **≥24 hours** for both success and failure; same key + different params → error; concurrent in-flight → not saved, client may retry; applies to POST (GET/DELETE ignore it) (source: https://docs.stripe.com/api/idempotent_requests, 2026-06).

**Relay implementation:** atomic `SET idem:{account}:{key}` with **NX + TTL** as an in-flight lock. NX success → process, then overwrite the marker with the serialized `{status, body}` and a ~24 h TTL. NX fail + stored response → replay it with the original status. NX fail + still in-flight → return **409**. Same key + different payload → **422**. This prevents double-sends to APNs/FCM under concurrent retries.

### 4.4 Provider auth recap (security view)

- **APNs:** cache one ES256 JWT, regenerate on a ~50-min timer (never < 20 min apart) under a mutex, reuse across senders; refresh on `403 ExpiredProviderToken`; back off on `429 TooManyProviderTokenUpdates`. Prefer team-scoped + topic-specific `.p8` keys (Feb 2025) for least privilege. Ensure the TLS trust store includes USERTrust RSA / SHA-2 root (§1.7). (sources as cited in §1.)
- **FCM:** cache the OAuth2 access token (~1 h) rather than minting per message. On GCP, prefer keyless auth (§4.5). (sources as cited in §2.)

### 4.5 Secret management

OWASP Secrets Management ranking — **KMS/Vault/secret-manager (best) > tightly-permissioned mounted secret file (acceptable) > plain env var (discouraged) > in image/repo (forbidden)**. Verbatim: "environment variables are generally accessible to all processes and may be included in logs or system dumps. Using environment variables is therefore not recommended unless the other methods are not possible." Encrypt at rest (AES-256-GCM or ChaCha20-Poly1305); "You should not store keys next to the secrets they encrypt" (envelope encryption / KMS); least privilege; prefer dynamic/short-lived secrets (source: https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html, 2026-06). Store the APNs `.p8` and FCM SA JSON in a secret manager; never commit or bake into the image.

**Google keyless direction (2025):** prefer Workload Identity / service-account impersonation / attached SA over downloadable JSON keys; org policy `constraints/iam.disableServiceAccountKeyCreation` blocks new keys. If the relay runs **on GCP**, use an attached SA / Workload Identity. If **off-GCP**, a JSON key is sometimes unavoidable — then it **must** live in a secret manager, not a repo or plain env var (source: https://docs.cloud.google.com/iam/docs/keys-disable-enable, 2026-06).

### 4.6 Log redaction

OWASP Logging exclude-list (verbatim items): "Access tokens"; "Authentication passwords"; "Session identification values"; "Database connection strings"; "Encryption keys and other primary secrets"; payment/PII. Redact by remove/mask/sanitize/hash/encrypt (source: https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html, 2026-06).

For the relay specifically — **never log**: the `Authorization`/bearer header, the raw API key (log only the non-secret **prefix** or a hash), full APNs/FCM **device tokens** (truncate/hash), or **notification payload bodies**. **Do log**: request ID, `account_id`/tenant ID, status code, error `reason`/`status`, and Retry-After/idempotency decisions for triage. Implement redaction in logging middleware so it can't be bypassed.

### 4.7 Multi-tenant isolation

Combine **noisy-neighbor resource control** (per-tenant rate/volume quotas at gateway/service/data tiers) with **per-tenant scoping** of every stored API key, rate-limit bucket, idempotency key, cached provider token, and **log line**. "Attach a tenant ID to every request, log entry, metric, and trace." Namespace all shared-store keys by `account_id` to prevent cross-tenant collision/replay. Cap downstream APNs/FCM spend per tenant (OWASP API4:2023). For APNs, recall the per-team connection-pool constraint (§1.1) (source: https://owasp.org/API-Security/editions/2023/en/0xa4-unrestricted-resource-consumption/, 2023).

```mermaid
flowchart TB
  T[Tenant request + API key + Idempotency-Key] --> AUTH[Auth middleware: prefix lookup + ConstantTimeCompare]
  AUTH --> RL[Per-tenant rate limit: Redis Lua token bucket]
  RL -->|429 + Retry-After| T
  RL --> IDEM[Idempotency: Redis SET NX lock / replay / 409 / 422]
  IDEM --> ROUTE{Provider}
  ROUTE -->|APNs| APNS[ES256 JWT cached ~50m, per-team HTTP/2 pool]
  ROUTE -->|FCM| FCM[OAuth2 token cached ~1h, v1 messages:send]
  APNS --> STORE[(pgx: tokens / keys / idempotency)]
  FCM --> STORE
  AUTH -. redacted .-> LOG[slog JSON: tenant, req-id, status, reason]
```

---

## 5. Open questions / re-verify before implementation

1. **APNs token page exact wording.** `establishing-a-token-based-connection-to-apns` is a JS-rendered SPA; content was confirmed via Apple's JSON data endpoint and corroborating sources. Re-read the live page to confirm exact 20/60-minute refresh wording and the scoped-key limits ("200 keys per env, up to 400 topics per topic-based key") near build time.
2. **`apns2` maintenance / bus factor.** No release since v0.25.0 (Oct 2024). Check recent commit/issue activity before committing to it vs. a hand-rolled `net/http2` + `golang-jwt/jwt/v5` client.
3. **APNs `SETTINGS_MAX_CONCURRENT_STREAMS`.** Intentionally variable and not documented by Apple. Measure at runtime; do not hardcode connection sizing.
4. **APNs sandbox hostname alias (resolved — interchangeable).** `api.sandbox.push.apple.com` and `api.development.push.apple.com` are interchangeable sandbox aliases: as of 2026-06-13 both resolve to the same Akamai CNAME and IP set (DNS-verified, §1.3), so either reaches the same sandbox backend. We standardize on `api.sandbox.push.apple.com` (canonical in Apple's docs and `apns2`'s `HostDevelopment`). No build-time blocker — this is a naming preference, not a discrepancy.
5. **APNs broadcast hosts.** If Live Activity broadcast/channels are in scope, confirm the broadcast-specific hosts (`api-broadcast.*`, `api-manage-broadcast.*` on 2195/2196) against Apple's code samples; the connection-section text in Apple's docs is internally inconsistent.
6. **Future APNs requirements.** None published after Feb 2025 as of June 2026 — re-check Apple's Upcoming Requirements page (e.g. a future TLS-1.3 minimum or another CA rotation) close to deployment.
7. **FCM `RetryInfo` on 429.** FCM's error-codes page promises only `google.rpc.QuotaFailure`, not `RetryInfo`. Treat `RetryInfo` as optional; fall back to `Retry-After` / 60 s default.
8. **FCM data-residency endpoints.** None for FCM as of June 2026 (global `fcm.googleapis.com` only). Re-check release notes if EU/other data-residency requirements emerge.
9. **FCM project quota tiers above 600K/min.** Confirm a project's real ceiling via Google Cloud Console quotas rather than assuming a secondary-source tier.
10. **App Standby Bucket caps.** Per-bucket high-priority wake-up caps are unpublished and OEM/version-dependent; confirm via on-device testing (`adb shell am set-standby-bucket` / `get-standby-bucket`) if precise behavior matters.
11. **Android 270-day token expiry.** Currently documented (manage-tokens, 2026-06-13) but a platform constant that can change — re-verify at build time.
12. **API-key "fast hash OK" citation.** Engineering consensus consistent with OWASP's passwords-vs-keys threat model, but not a single verbatim OWASP line for API keys; if a hard citation is required, point to that distinction, and decide plain salted SHA-256 vs HMAC-SHA-256 pepper (pepper adds defense-in-depth but complicates rotation).
13. **Idempotency-Key draft status.** Draft 07 expired 2026-04-18 without becoming an RFC; re-check for an 08+/RFC before relying on its exact status-code semantics as "standard."
14. **GCP vs off-GCP for FCM credentials.** Whether the relay runs on GCP (keyless Workload Identity) or off-GCP (JSON key in a secret manager) changes the recommended FCM credential approach — resolve in the deployment design.
