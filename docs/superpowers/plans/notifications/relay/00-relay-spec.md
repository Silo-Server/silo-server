# Silo Push Relay — Engineering Spec

**Date:** 2026-06-13
**Status:** Draft (implementation-grade)
**Service:** `silo-push-relay` (provisional repo name; the relay code lives in a **separate repository**)
**Provisional hostname:** `relay.silo.app`
**Stack:** Go 1.25+, PostgreSQL, Redis

**Provenance / source contracts (read these first):**

- [`../00-architecture-overview.md`](../00-architecture-overview.md) — cross-cutting design; "Mobile push: why a relay is necessary", addressing-flow walkthrough, mode terminology, cross-channel threat model.
- [`../02-apns-relay.md`](../02-apns-relay.md) — **the external APNs relay contract** (`POST /v1/apple/send`): request/response/validation, relay-built payloads, headers, error handling. The self-hosted Silo server is already designed against this; the relay is its counterpart and must honor it exactly.
- [`../03-fcm-relay.md`](../03-fcm-relay.md) — **the external FCM relay contract** (`POST /v1/fcm/send`): request/response/validation, relay-built data-only payloads, Android config, error handling.
- [`./02-apns-fcm-2026-reference.md`](./02-apns-fcm-2026-reference.md) — the 2026-current Apple/Google/Go reference this service is built on. Where the older contract docs (authored 2026-04) glossed an upstream-provider mechanic, **this reference wins for the upstream side**; the **external relay contract from 02/03 is never changed** — reconciliations are called out explicitly in §5.2 (FCM upstream field casing), §6.2 (sandbox host), §6.4 (APNs error mapping), and §7.5.

> **Commands assume the repository root is the cwd.** No local absolute filesystem paths or transient worktree IDs appear in this document; all repo references are repository-relative.

---

## 1. Title, Provenance, Status, Scope

### 1.1 What this service is

`silo-push-relay` is a small, **stateless-on-the-request-path** HTTP service that:

1. Holds the official Silo Apple (`.p8` APNs auth key) and Google (Firebase service-account JSON) push credentials.
2. Accepts authenticated, **opaque, content-free** push requests from opted-in self-hosted Silo servers on two endpoints: `POST /v1/apple/send` and `POST /v1/fcm/send`.
3. Builds a fixed, generic APNs / FCM payload from those opaque fields and forwards it to APNs / FCM on the caller's behalf.
4. Maps the upstream provider response back to a narrow caller-facing result.

It is the **server counterpart** to the relay-client integration in `02-apns-relay.md` and `03-fcm-relay.md`. Those two documents define the wire contract from the self-hosted Silo server's perspective; this document specifies the service that answers it. **The external request/response contract in 02/03 is authoritative and is reproduced here byte-for-byte; this spec adds the server-internal design (upstream clients, storage, auth, rate limiting, idempotency, ops) that 02/03 deliberately left to "a separate repo".**

### 1.2 What this service is NOT

- It is **not** a Silo media server, and it shares no database, auth system, or deployment with `silo-server`.
- It is **not** a general-purpose push gateway: it accepts only the narrow JSON shape in §5; it never accepts a free-form APNs/FCM payload, title, body, or media identifier.
- It is **not** an addressing service. Apple and Google do the addressing from the device token. The relay never resolves a token to a user, profile, device-to-user mapping, or server.
- It does **not** store notification content, user/profile identity, server URLs, or token→user aliases (v1 is stateless on the request path; see `02-apns-relay.md` "Relay statefulness: stateless v1").
- It is **not** in the `custom_apns` / `custom_fcm` path. When a self-hosted admin configures their own credentials, the Silo server talks to APNs/FCM directly and never touches this relay (see `00-architecture-overview.md` "Custom credentials").

### 1.3 Relationship to the contract docs and the separate repo

| Concern | Owner | Document |
|---|---|---|
| External relay request/response/validation/payloads | `02`/`03` (authoritative) | `../02-apns-relay.md`, `../03-fcm-relay.md` |
| Self-hosted server's relay-client, dispatcher, pacing, retry | `silo-server` | `../02`, `../03`, `../00` |
| Relay service internals (this doc): upstream clients, DB, auth, rate limit, idempotency, deploy | `silo-push-relay` (separate repo) | **this file** |
| 2026-current upstream provider mechanics (JWT, OAuth2, endpoints, error tables) | reference | `./02-apns-fcm-2026-reference.md` |

These design docs are authored **here in `silo-server`** alongside the `02`/`03` contracts purely for co-location. The relay **code** lives elsewhere (`silo-push-relay`).

---

## 2. Goals and Non-Goals

### 2.1 Goals

- **G1 — Faithful contract.** Implement `POST /v1/apple/send` and `POST /v1/fcm/send` exactly as `02`/`03` specify: same request fields, same validation rules, same response shape, same relay-built payloads, same headers.
- **G2 — Content-free by construction.** The request schema admits **only** the opaque fields in §5. Any unexpected/free-form field is rejected with `400`. The relay cannot forward content even if a caller tries.
- **G3 — Credential custody.** Hold the official `.p8` and SA JSON in a secret manager, never on disk in the repo/image, never in env vars where avoidable (§11).
- **G4 — Stateless request path.** No per-request DB writes other than throttled `last_used_at` and redacted op-logs. Idempotency + rate-limit state live in Redis. The relay can be horizontally scaled and a single instance can be killed mid-flight without data loss (the server-side outbox in `../01-release-events-and-inbox.md` guarantees re-delivery).
- **G5 — Correct upstream mechanics (2026).** One cached ES256 JWT per APNs team (regenerated ~50 min, never < 20 min apart); one cached OAuth2 access token per FCM project (~1 h, refresh with margin); HTTP/2 connection reuse with PING health checks; correct reason→error mapping. Per `./02-apns-fcm-2026-reference.md`.
- **G6 — Multi-tenant isolation.** Every API key, rate-limit bucket, idempotency key, and log line is scoped by `account_id`. Per-account allowlists bound which APNs topics / FCM (project, package) an account may push to.
- **G7 — Minimal attack surface for admin ops.** No public admin HTTP API in v1. Account/key/allowlist administration is a CLI (`relayctl`) writing directly to the DB (§5.6).
- **G8 — Operable.** Structured redacted logs, Prometheus metrics, `/healthz` + `/readyz`, graceful shutdown.

### 2.2 Non-goals

- **NG1** — Storing token→user aliases, device subscriptions, or any user/profile identity (deferred; `02` "stateless v1").
- **NG2** — Accepting arbitrary APNs/FCM JSON, custom titles/bodies, badge by default, or topic broadcasts (FCM `topic`/`condition` are never used — per-token only, per `03`).
- **NG3** — Delivery receipts, analytics, marketing exports, or open-tracking.
- **NG4** — APNs broadcast / Live Activity channels (`/4/broadcasts/...`) — out of scope; see `./02-apns-fcm-2026-reference.md` §1.10. v1 uses only `/3/device/<token>`.
- **NG5** — Web Push, HMS, ADM, or any non-APNs/non-FCM transport.
- **NG6** — A public self-service signup / billing surface. Accounts are provisioned by Silo operators via `relayctl`.
- **NG7** — The `custom_apns` / `custom_fcm` direct paths (those run inside `silo-server`, not here).

---

## 3. Trust and Threat Model

### 3.1 What the relay sees (restated from `00`/`02`/`03`)

The relay, even fully compromised, sees only:

- **Which self-hosted installation** is calling — the relay API key (`rk_…`) maps 1:1 to a relay account.
- **The server's egress IP** — inherent to any hosted relay; the server connects directly. For home hosting this identifies the household connection. **This is the closest thing to an identity leak in the design** and is listed so the privacy claim stays exhaustive, not over-strong (`00` addressing-flow note).
- **A device token** — APNs device token (~100 bytes hex) or FCM registration token (~152–180+ chars).
- **Request timing.**
- **Coarse wire mode** — `private_alert`/`background_wake` (APNs) or `private_data`/`background_wake` (FCM).
- **Opaque `collapse_id` / `collapse_key`** — per-server-keyed HMACs (see §3.3).
- **Opaque correlation IDs** — `server_device_id`, `delivery_id`.
- **Upstream response status** — APNs `apns-id` / FCM message name, status code, reason string.
- **`apns-topic` / FCM (`project_id`, `package_name`)** — platform/build identifiers Apple/Google already see (`02` privacy note).

### 3.2 What the relay must NEVER see or store

Per `00`/`02`/`03`: no notification title or body, no media identifiers, no profile/user/username/library/collection/item names, no server hostname or base URL, no artwork URLs, no watched-state metadata, no device→user mapping, and **no token aliases** (stateless v1). The request schema (§5) has no field that could carry any of these, and an unknown field is a hard `400` (§12).

### 3.3 Collapse-ID equivalence classes (opaque to the relay)

The Silo server computes `collapse_id = base32(HMAC-SHA256(server_collapse_secret, series_id))` truncated to 26 chars (APNs) / equivalent for FCM `collapse_key`, where `server_collapse_secret` is a random per-server secret (`02`/`03`). The relay treats this as **opaque** and never derives it. Residual leakage: the relay can group **one server's** pushes into per-series equivalence classes (that is what collapse is *for*), but the per-server key prevents recovering the series identity or correlating the same series across two different servers.

### 3.4 Residual leakage (exhaustive)

| Leak | To whom | Why it's irreducible | Mitigation |
|---|---|---|---|
| Egress IP | Relay operator | Server connects directly over HTTPS | Admin may route relay traffic through their own VPN/proxy egress (`02` residual-leakage note) |
| Push timing | Relay operator + Apple/Google | A push is an observable network event | None — only meaning/content is hidden |
| Device token | Relay operator + Apple/Google | Required to address the device | Tokens never logged raw; truncated/hashed (§11/§12) |
| Collapse equivalence class | Relay operator | Collapse must be a stable per-series key | Per-server HMAC key prevents series recovery & cross-server correlation (§3.3) |
| App topic / Firebase project + package | Relay operator + Apple/Google | Required by the upstream APIs | Platform/build identifiers Apple/Google already hold |

The relay **cannot** learn user identity, notification content, server URL, or library data even when fully compromised (`00` threat-model summary). It **cannot fabricate meaningful pushes** — only generic opaque wake payloads.

### 3.5 Adversaries

| Adversary | Capability | Worst case | Control |
|---|---|---|---|
| Hostile/compromised relay operator | Full DB + request stream | Learns device tokens, timing, egress IPs of opted-in servers; can send generic wakes | No content/identity; per-server collapse keying; opt-in & disabled by default |
| Stolen relay API key | Can send opaque pushes as that account | Generic wakes to tokens the thief already holds; bounded by per-account allowlist + rate limits | Revoke key (`relayctl key revoke`); rotate; per-account quota caps spend (OWASP API4:2023) |
| Cross-tenant attacker | Tries to push to another account's topic/token | Rejected: topic/project/package not on attacker's allowlist | Per-account allowlist enforcement (§8.4); namespaced Redis keys |
| Apple / Google | Platform operator | Sees topic, token, generic payload, timing | No identity/content (data-only FCM; loc-key-only APNs alert) |

---

## 4. Architecture

### 4.1 Request path (stateless)

```
1. TLS terminate (LB or in-process) → HTTP/2 inbound.
2. Auth middleware: parse "Authorization: Bearer rk_<prefix>_<secret>";
   SELECT row by indexed prefix; constant-time-compare hash; reject revoked/expired.
3. Rate-limit middleware: per-account Redis token bucket (Lua: refill→check→consume).
   Over limit → 429 + Retry-After.
4. Body decode with DisallowUnknownFields (any unexpected field → 400).
5. Validate fields (§5) incl. allowlist check (topic / project+package).
6. Idempotency: Idempotency-Key header → Redis SET NX lock; replay / 409 / 422.
7. Build the fixed relay payload (§5) + headers; forward to upstream APNs/FCM
   client (cached JWT / OAuth2 token, reused HTTP/2 conn).
8. Map upstream response → caller-facing JSON (§5).
9. Store the {status, body} under the idempotency key (TTL ~24h);
   throttled last_used_at update; emit redacted op-log + metrics.
```

The only synchronous DB touch on the hot path is the **API-key lookup** (indexed, O(1) by prefix) and, at most once per minute per key, a `last_used_at` write. Idempotency and rate-limit state are in Redis. No notification content, no token alias, ever persisted.

### 4.2 Component diagram

```mermaid
flowchart TB
  subgraph Server["Self-hosted Silo server (caller)"]
    DISP[Push dispatcher + client-side pacing\n5 req/s token bucket]
  end

  DISP -->|"POST /v1/apple/send  Bearer rk_…  Idempotency-Key"| LB[(TLS / Load balancer)]
  DISP -->|"POST /v1/fcm/send"| LB

  subgraph Relay["silo-push-relay (stateless replicas)"]
    LB --> API[HTTP server\nnet/http, HTTP/2, timeouts]
    API --> AUTH[Auth middleware\nprefix lookup + ConstantTimeCompare]
    AUTH --> RL[Rate-limit middleware\nRedis Lua token bucket]
    RL --> IDEM[Idempotency\nRedis SET NX / replay / 409 / 422]
    IDEM --> VAL[Validate + allowlist\nDisallowUnknownFields]
    VAL --> AC[APNs client\ncached ES256 JWT ~50m\nper-team HTTP/2 pool]
    VAL --> FC[FCM client\ncached OAuth2 token ~1h\nHTTP/2]
    API --> OPS[/healthz /readyz /metrics/]
    AUTH -. redacted .-> LOG[slog JSON]
  end

  AC -->|"POST /3/device/<token>"| APNS[(api.push.apple.com\n/ api.sandbox.push.apple.com)]
  FC -->|"POST /v1/projects/{id}/messages:send"| FCM[(fcm.googleapis.com)]

  AUTH --> PG[(PostgreSQL\naccounts / api_keys /\nallowlists / op_logs)]
  RL --> REDIS[(Redis\nidempotency + rate buckets)]
  IDEM --> REDIS

  CLI[relayctl CLI\naccount/key/allowlist admin] --> PG
  SM[(Secret manager)] -.->|.p8 / SA JSON / pepper| AC
  SM -.-> FC
```

### 4.3 Data stores

| Store | Holds | Why | Notes |
|---|---|---|---|
| **PostgreSQL** | `relay_accounts`, `relay_api_keys`, `relay_apns_allowlist`, `relay_fcm_allowlist`, `relay_op_logs` | Durable, low-write config + redacted audit | `jackc/pgx/v5` + `pgxpool` (`./02-apns-fcm-2026-reference.md` §3.4). Never stores tokens, content, or identity. |
| **Redis** | `idem:{account}:{key}` (idempotency lock/result), `rl:{account}` (token-bucket state) | Fast, TTL'd, shared across replicas | `redis/go-redis/v9`. Atomic Lua for rate-limit; `SET NX` for idempotency. Idle keys TTL out. |
| **Secret manager** | APNs `.p8`, FCM SA JSON, API-key HMAC pepper, DB/Redis creds | Credential custody | KMS/Vault/secret-manager preferred over mounted file over env var (OWASP, `./02-apns-fcm-2026-reference.md` §4.5/§5). |

### 4.4 Credential holding

- **APNs:** one (or a few, scoped) `.p8` ES256 keys + Team ID + Key ID per official team. Prefer team-scoped + topic-specific keys (Feb-2025 hardening) for least privilege (`./02-apns-fcm-2026-reference.md` §1.2). A connection pool is **per APNs team** (a connection cannot serve multiple teams — §1.1 of the reference).
- **FCM:** one service-account JSON per official Firebase project (`continuum-prod-android`). On GCP, prefer keyless Workload Identity / attached SA; off-GCP, the JSON lives in the secret manager (`./02-apns-fcm-2026-reference.md` §2.2/§4.5).
- Credentials are loaded **once at startup** from the secret manager into memory; never written to disk, never logged, never returned by any endpoint. Reload requires a process restart (v1) or SIGHUP-triggered re-read (optional).

---

## 5. Public API Contract

All endpoints are HTTPS-only over HTTP/2. Authentication is `Authorization: Bearer rk_<key>` (§8.2). `Content-Type: application/json` for send endpoints. Bodies are decoded with `DisallowUnknownFields` — **any unrecognized field is a `400`** (§12). Unless stated otherwise, error bodies use the shape in §5.5.

### 5.1 `POST /v1/apple/send`

Authoritative source: `02-apns-relay.md` "Send Apple Push". Reproduced exactly.

**Request**

```http
POST /v1/apple/send HTTP/2
Host: relay.silo.app
Authorization: Bearer rk_live_3Qw9...secret...
Idempotency-Key: 01JDELIVERY...:01JDEVICE...:1
Content-Type: application/json
```

```json
{
  "token": "apns-token",
  "environment": "production",
  "topic": "com.continuum.app.ios",
  "mode": "private_alert",
  "server_device_id": "01JOPAQUE...",
  "delivery_id": "01JDELIVERY...",
  "badge": null,
  "collapse_id": "01JOPAQUE_COLLAPSE"
}
```

**Field table**

| Field | Type | Required | Rule |
|---|---|---|---|
| `token` | string | yes | Required; must be plausible for APNs — non-empty, hex-shaped (`^[0-9a-fA-F]+$`). Apple device tokens are commonly **64 hex chars (32 bytes)** but Apple does not guarantee a fixed length and has lengthened them historically, so the relay enforces a **generous bound (e.g. ≥ 64 and ≤ 256 hex chars)** rather than a tight guess that could reject valid future-length tokens. Reject non-hex / out-of-bound → `400 invalid_token`. |
| `environment` | string | yes | `production` or `sandbox`. Selects upstream host (§6.2). Any other value → `400 invalid_environment`. |
| `topic` | string | yes | Must be on the account's APNs topic allowlist (§8.4). Not allowlisted → `403 topic_not_allowed`. |
| `mode` | string | yes | `private_alert` or `background_wake`. Other → `400 invalid_mode`. |
| `server_device_id` | string | yes | Opaque; length-limited (≤128 chars, ULID-shaped recommended). Over limit → `400 invalid_field`. |
| `delivery_id` | string | yes | Opaque; length-limited (≤128). |
| `badge` | integer \| null | no | Default omitted. When present, must be a **bounded non-negative integer `0 ≤ badge ≤ 9999`** — out of range → `400 invalid_field`. This is the one numeric field copied verbatim into `aps.badge`, so it is bounded to keep it from becoming an unbounded caller-controlled channel visible to Apple. Badge is **disabled by default** to avoid leaking unread counts (`02` privacy); the relay forwards it only if provided. |
| `collapse_id` | string \| null | no | Opaque; **≤ 64 bytes** (Apple's `apns-collapse-id` cap). Over 64 bytes → `400 invalid_collapse_id`. |

No other fields are accepted: **no title, body, image URL, media ID, username, server hostname, or server URL** field exists (`02`). An unknown key → `400 unexpected_field`.

**`Idempotency-Key` header** (required for sends): format `<notification_delivery_id>:<push_device_id>:<attempt_number>` (`02`/`03`). See §10.

**Response — 200**

```json
{
  "request_id": "01JRELAY...",
  "apns_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "accepted"
}
```

- `request_id` — relay-generated ULID, also echoed as the `X-Request-Id` response header and used in logs.
- `apns_id` — Apple's `apns-id` response header (UUID).
- `status` — `accepted` on a 200 from APNs.

> **Sandbox `apns-unique-id` (additive observability).** On a **200 in the sandbox/DEVELOPMENT environment only**, APNs additionally returns an `apns-unique-id` response header used by the Push Notifications Console Delivery Log (reference §1.6). It is the single most useful field for diagnosing "accepted-but-not-delivered" on sandbox. The relay **captures `apns-unique-id` (sandbox only)** into the op-log and the `relayctl ping-upstream` output, and may echo it in the sandbox 200 body, to aid delivery-log correlation during client QA. It is **never** present or logged for production sends.

**Relay-built APNs payloads** (the relay constructs these; callers cannot override — `02`).

`private_alert`:

```json
{
  "aps": {
    "alert": {
      "title-loc-key": "SILO_NOTIFICATION_TITLE",
      "loc-key": "SILO_NOTIFICATION_GENERIC_BODY"
    },
    "sound": "default"
  },
  "silo": {
    "v": 1,
    "wake": "notifications.changed",
    "server_device_id": "01JOPAQUE...",
    "delivery_id": "01JDELIVERY..."
  }
}
```

When `badge` is present and non-null, the relay adds `"badge": <n>` inside `aps`. Otherwise it is omitted entirely (no `badge` key).

`background_wake`:

```json
{
  "aps": { "content-available": 1 },
  "silo": {
    "v": 1,
    "wake": "notifications.changed",
    "server_device_id": "01JOPAQUE...",
    "delivery_id": "01JDELIVERY..."
  }
}
```

**Exact APNs request headers the relay sets** (`02` Headers + reconciled with `./02-apns-fcm-2026-reference.md` §1.4):

| Upstream header | `private_alert` | `background_wake` | Source |
|---|---|---|---|
| `:method` | `POST` | `POST` | fixed |
| `:path` | `/3/device/<token>` | `/3/device/<token>` | `token` field |
| `authorization` | `bearer <cached ES256 JWT>` | same | relay JWT (§6.1) |
| `apns-topic` | the allowlisted `topic` | same | `topic` field |
| `apns-push-type` | `alert` | `background` | `02` |
| `apns-priority` | `10` | `5` | `02` (and reference §1.4.1: background **must** be 5; 10 is an error) |
| `apns-collapse-id` | `collapse_id` if present (≤64B) | same | `02` |
| `apns-id` | omitted (Apple auto-generates; echoed back) | same | reference §1.4 |
| `apns-expiration` | **finite default** (e.g. `now + 4 h`) | **short TTL** (e.g. `now + 1 h`) | reference §1.4. **Configurable**; ships a sane non-30-day default — see below. |

> **`apns-expiration` policy (v1 ships a finite default, not the 30-day store-and-retry).** With `apns-expiration` omitted, Apple stores and retries an undelivered push for up to **30 days** (reference §1.4) — a wake fired days later is useless or confusing for content that is fetched live from the user's server on wake (the underlying notification may already be read/deleted server-side). The relay therefore sets a **finite, configurable `apns-expiration`**: a few hours for `private_alert` (default ~4 h) and a **short** TTL for `background_wake` (default ~1 h), so a device offline for days does not get a stale wake. Both defaults are config values (per-deployment); the relay never ships the 30-day default. The FCM path sets the parallel `android.ttl` (§5.2) for parity. (Resolved from former open question §15 #5; values are tunable from product input.)

Payload is **uncompressed JSON**, ≤ 4 KB (reference §1.5). The relay-built payloads are far under 4 KB; the relay still rejects with `400 payload_too_large` defensively if a (future) field pushes it over, and maps an upstream `413` per §6.4.

**Error mapping back to the caller** — see §6.4.

### 5.2 `POST /v1/fcm/send`

Authoritative source: `03-fcm-relay.md` "Send Android Push". Reproduced exactly.

**Request**

```http
POST /v1/fcm/send HTTP/2
Host: relay.silo.app
Authorization: Bearer rk_live_3Qw9...secret...
Idempotency-Key: 01JDELIVERY...:01JDEVICE...:1
Content-Type: application/json
```

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

**Field table**

| Field | Type | Required | Rule |
|---|---|---|---|
| `token` | string | yes | Plausible FCM token: non-empty, base64url-ish, **length 100 ≤ len ≤ 4096** (FCM tokens are typically 152–180+ chars — `03`; the upper bound guards against oversized-token abuse without rejecting legitimate longer tokens). Implausible → `400 invalid_token`. |
| `project_id` | string | yes | Must match the configured Firebase project for this account's allowlist (§8.4). Mismatch → `403 project_not_allowed`. |
| `package_name` | string | yes | Must be on the account's FCM package allowlist, paired with `project_id` (§8.4). Not allowlisted → `403 package_not_allowed`. |
| `mode` | string | yes | `private_data` or `background_wake` (the **wire** mode; profile-level is `private_push` — `00`/`03`). Other → `400 invalid_mode`. |
| `server_device_id` | string | yes | Opaque; ≤128 chars. |
| `delivery_id` | string | yes | Opaque; ≤128 chars. |
| `badge` | integer \| null | no | Accepted for caller-contract parity with the APNs endpoint, but **FCM has no data-only home for it** (the relay never builds a `notification` block). v1 **ignores `badge` on the FCM path** — it is neither rendered nor smuggled into the `data` map (§5.2 construction rules, §12). When present it must still pass the same bound as APNs (`0 ≤ badge ≤ 9999`); otherwise → `400 invalid_field`. |
| `collapse_key` | string \| null | no | Opaque; **≤ 64 bytes**. This is the **inbound caller field name** (snake_case, per `03`); the relay maps it to the FCM REST field `android.collapseKey` (camelCase — see §5.2 construction rules). Over 64 bytes → `400 invalid_collapse_key`. |

No free-form `notification`, title, body, image URL, media ID, username, or server URL field exists (`03`). Unknown key → `400 unexpected_field`.

**Response — 200**

```json
{
  "request_id": "01JRELAY...",
  "fcm_message_name": "projects/continuum-prod-android/messages/0:1234567890123456%abcdef",
  "status": "accepted"
}
```

- `fcm_message_name` — FCM v1 response `name`.

**Relay-built FCM payloads — data-only, no `notification` block** (`03`).

`private_data`:

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
      "priority": "HIGH",
      "collapseKey": "01JOPAQUE_COLLAPSE"
    }
  }
}
```

`background_wake`:

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
      "priority": "NORMAL",
      "collapseKey": "01JOPAQUE_COLLAPSE"
    }
  }
}
```

**Construction rules (reconciled with `./02-apns-fcm-2026-reference.md` §2.3/§2.4):**

- `message.data` is `map<string,string>` — **all values are strings** (`"v": "1"`, not `1`). It is a **fixed, closed set of keys** — exactly `{v, wake, server_device_id, delivery_id}` — with **no caller-controlled key or value** beyond the already-opaque IDs (§12). Reserved keys (`from`, `message_type`, `notification`, `google.*`, `gcm.*`) are never used, and `badge` is never injected into `data`.
- No top-level `message.notification` — **data-only** so Google carries no rendering content (`03`).
- `android.priority`: `"HIGH"` for `private_data` (wakes under Doze), `"NORMAL"` for `background_wake`. The relay emits the **canonical uppercase enum** values documented for FCM REST `AndroidConfig.priority` (reference §2.4); FCM also accepts lowercase, but the canonical form is used to avoid relying on a parenthetical allowance. **Reconciliation:** the `03` contract reproduces lowercase `high`/`normal`; the wire field accepts both and `priority` is relay-set (not a caller field), so this is an internal-upstream normalization with no external-contract impact.
- `android.collapseKey` (REST **camelCase** — reference §2.4): set to the caller's `collapse_key` when present, omitted otherwise. **Maps to FCM `android.collapseKey`** — the relay POSTs raw JSON to the FCM HTTP v1 REST endpoint, which rejects unknown fields (including a snake_case `collapse_key` inside `android`) with `INVALID_ARGUMENT`. **Reconciliation (parallel to the sandbox-host one in §6.2):** the inbound caller field stays `collapse_key` per `03`/§5.2; only the relay-built upstream FCM field is `collapseKey`.
- `android.ttl` (a duration string, e.g. `"14400s"`): set to a **finite, configurable** TTL for parity with the APNs `apns-expiration` policy (FCM otherwise defaults to **4 weeks** of storage). Default ~4 h for `private_data`, a shorter TTL for `background_wake`, so a device offline for days does not get a stale wake. Configurable per deployment.
- `validateOnly` (top-level, **camelCase** — reference §2.5) is the dry-run flag for the `relayctl` test path only (§7.3, §7.5); never set on production sends.
- The relay never sets FCM `topic`/`condition`; only `message.token` (per-token only — `03`).
- Payload ≤ 4096 bytes for token sends (reference §2.3); the fixed payload is far under.

> **Reconciliation note (priority vs. reference §2.8).** The reference warns that high-priority data messages that never produce a user-facing notification can be deprioritized to normal by FCM over a ~7-day window. The `03` contract nonetheless specifies `priority: high` for `private_data` (the app *does* surface a notification after wake), and the relay **honors the `03` contract** — it does not second-guess priority. Deprioritization is an upstream behavior, not a contract change; the `03` doc already documents the quota fallback (caller may switch to normal after repeated 429s — §7.4).

**Exact FCM request** the relay sends:

```http
POST /v1/projects/continuum-prod-android/messages:send HTTP/2
Host: fcm.googleapis.com
Authorization: Bearer <cached OAuth2 access token>
Content-Type: application/json
```

**Error mapping** — see §7.4.

### 5.3 `GET /healthz`

Liveness. Returns `200 {"status":"ok"}` if the process is up. No dependency checks. Used by the orchestrator's liveness probe. Unauthenticated.

### 5.4 `GET /readyz` and `GET /metrics`

- **`GET /readyz`** — readiness. Readiness gates on **core shared dependencies only**: Postgres reachable (`SELECT 1` via pool) and Redis reachable (`PING`). Per-provider health (APNs signer initialized; FCM token source initialized) is reported **separately, not as a binary gate**, so that a **single-provider** credential/init failure does **not** pull the whole replica from the LB and kill the healthy provider too (APNs and FCM are independent upstreams; the "channel failures are isolated" principle from `00` carried into the relay's own health model). The body is `200 {"status":"ready","providers":{"apns":"healthy|degraded","fcm":"healthy|degraded"}}` when core deps are up, or `503 {"status":"not_ready","checks":{...}}` only when **PG or Redis** is down. When a provider is `degraded`, the **send handler for that provider** returns `503 upstream_unavailable` for that provider only (the other provider keeps serving); a per-provider gauge and circuit-breaker state make `APNs degraded, FCM healthy` visible to operators rather than a binary not-ready. See §13.2 (`relay_provider_healthy`).
- **`GET /metrics`** — Prometheus exposition (`promhttp`). Bound to the internal network / scrape-only. See §13.2 for the catalog.

### 5.5 Standard error body

All 4xx/5xx (except health endpoints) return:

```json
{
  "error": {
    "code": "topic_not_allowed",
    "message": "topic is not on this account's allowlist",
    "request_id": "01JRELAY..."
  }
}
```

`code` is a stable machine string (see §6.4 / §7.4 tables). `request_id` matches `X-Request-Id`. `Retry-After` is set on `429`/`503` (§9, §10).

### 5.6 Admin surface — `relayctl` CLI (no public admin HTTP API)

To minimize attack surface (G7), there is **no HTTP admin API** in v1. Administration is a Go CLI, `relayctl`, that connects **directly to the relay's PostgreSQL** (via a privileged DSN held by operators only) and performs the operations below. It runs from an operator workstation or a jump host — never exposed to the public internet.

| Command | Action | DB effect |
|---|---|---|
| `relayctl account create --name <label> [--note <text>]` | Provision a relay account (one per opt-in installation). Prints the new `account_id`. | INSERT `relay_accounts`. |
| `relayctl account list [--active]` | List accounts with status, key count, last activity. | SELECT. |
| `relayctl account disable --account <id>` | Suspend an account (all keys rejected, but rows retained). | UPDATE `relay_accounts.status='disabled'`. |
| `relayctl key issue --account <id> [--env live\|test] [--expires <dur>]` | Generate a new API key. **Prints the full `rk_…` token exactly once** (only the hash is stored). | INSERT `relay_api_keys`. |
| `relayctl key list --account <id>` | List keys (prefix, created, last_used, revoked) — never the secret. | SELECT. |
| `relayctl key revoke --key-prefix <prefix>` | Revoke a key immediately. | UPDATE `relay_api_keys.revoked_at=now()`. |
| `relayctl allowlist apns set --account <id> --topics com.continuum.app.ios,.tvos,.macos` | Replace the account's APNs topic allowlist. | UPSERT `relay_apns_allowlist`. |
| `relayctl allowlist fcm set --account <id> --project continuum-prod-android --packages com.continuum.app.android` | Replace the account's FCM (project, packages) allowlist. | UPSERT `relay_fcm_allowlist`. |
| `relayctl allowlist show --account <id>` | Print effective allowlists. | SELECT. |

`relayctl` operations are themselves audited into `relay_op_logs` (event `admin.*`) with the operator identity (OS user / `--actor`) but never the secret token. The CLI shares the relay's data-access package so schema and constraints stay in one place.

---

## 6. Upstream APNs Client

Built per `./02-apns-fcm-2026-reference.md` §1 and §3.1. Library choice: `github.com/sideshow/apns2 v0.25.0` **or** a hand-rolled `net/http` + `golang.org/x/net/http2` + `golang-jwt/jwt/v5` client (reference §3.1; bus-factor caveat in §15). Either way, the mechanics below are required.

### 6.1 Auth — JWT ES256 lifecycle

- **Algorithm:** ES256 (ECDSA P-256) **only** — APNs accepts no other (reference §1.1).
- **Header:** `{"alg":"ES256","kid":"<10-char Key ID>"}`. **Claims:** `{"iss":"<10-char Team ID>","iat":<epoch seconds>}`.
- **Signing key:** the official `.p8` private key from the secret manager.
- **Caching:** generate **one** JWT, hold it in memory behind a mutex, reuse across all senders/connections/topics for that team (reference §1.1/§4.4).
- **Refresh cadence:** regenerate on a **~50-minute timer** (well inside Apple's 1-hour `iat` window), **never more often than once per 20 minutes**, and eagerly on a `403 ExpiredProviderToken` (then retry the request once). Over-frequent refresh → `429 TooManyProviderTokenUpdates`; back off and slow refresh (reference §1.1 lifecycle table).
- **Single-flight regeneration (stampede guard).** Under a burst of concurrent sends sharing one cached JWT, an expiry or a `403 ExpiredProviderToken` is observed by **many in-flight goroutines at once**. JWT regeneration **must be single-flighted** — e.g. `golang.org/x/sync/singleflight` keyed by team, or a generation counter behind the mutex: a goroutine seeing `ExpiredProviderToken` checks whether the JWT was **already refreshed since its request started** (compare its captured generation/`iat` to the current one) and, if so, **reuses the new token** instead of triggering another refresh. Only **one** refresh happens per ~20-min window regardless of how many concurrent 403s arrive. A naive per-goroutine regenerate-on-403 would trip `429 TooManyProviderTokenUpdates`; the 20-minute floor is honored **by this concurrency mechanism**, not just asserted. (Mirrors risk R3.)
- **Per-team pools.** A connection cannot map to multiple teams; open a separate JWT + HTTP/2 connection pool per APNs team (reference §1.1). v1 has one official team, so one pool — but the code keys pools by team to stay correct.
- **Key selection — `(environment, topic) → .p8 key`.** With Apple's Feb-2025 team-scoped/topic-specific keys (max 2 team-scoped keys per environment), more than one `.p8` may be loaded (e.g. a topic-specific key for `com.continuum.app.ios` plus a team key; or per-environment scoped keys). A **sandbox send must use a sandbox-scoped key and a production send a production-scoped key** — using the wrong one yields `403 BadEnvironmentKeyIdInToken` / `UnrelatedKeyIdInToken`. The relay therefore holds an explicit **key-selection map `(environment, topic) → key id`** in config, falling back to an unscoped team key only where no scoped key exists. **Startup validation:** every allowlisted `(topic, environment)` must resolve to a usable key, or startup fails. A `BadEnvironmentKeyIdInToken`/`UnrelatedKeyIdInToken` at runtime maps to `upstream_auth_error` (§6.4) and is treated as a **key-selection misconfiguration** (ops alert).

### 6.2 Endpoint selection

| `environment` field | Upstream host | Port |
|---|---|---|
| `production` | `https://api.push.apple.com` | 443 (2197 fallback) |
| `sandbox` | `https://api.sandbox.push.apple.com` | 443 (2197 fallback) |

Both hosts are **hardcoded constants** selected only by the `environment` enum — never derived from any other request field (§11 egress/SSRF posture).

> **Reconciliation (host name).** `02-apns-relay.md` validation text names the sandbox host `https://api.development.push.apple.com`. **Both `api.sandbox.push.apple.com` and `api.development.push.apple.com` are valid, functionally-equivalent Apple sandbox aliases — both resolve.** The relay **standardizes on `api.sandbox.push.apple.com`** (reference §1.3; it also matches `sideshow/apns2 v0.25.0`'s `HostDevelopment` constant, which points at `api.sandbox.push.apple.com`). This is an *internal upstream* detail and does **not** change the external contract: callers still send `"environment": "sandbox"` exactly as `02` specifies. (See reference open question §5 item 4 / this spec §15 — now a note that the two hosts are interchangeable, not a discrepancy to resolve at build time.)

### 6.3 Connection management

- HTTP/2 + TLS 1.2+ (1.3 acceptable). **Reuse one client / connection pool per process per environment per team**; never create a client per push (TLS setup is slow — reference §3.1). One well-tuned connection can do thousands of pushes/sec.
- Set a non-zero `ReadIdleTimeout` (~15 s) + `PingTimeout` (~15 s) on the `http2.Transport` to reap half-open connections (reference §3.3 — "the single most important tuning for a long-lived relay").
- Do **not** hardcode `SETTINGS_MAX_CONCURRENT_STREAMS`; read it from the server (reference §1.3/§3.3).
- Handle `GOAWAY`: drop the connection from the pool and reconnect; finish in-flight streams ≤ `Last-Stream-ID` (RFC 9113 §6.8). Go's `net/http2` transport does most of this when one client is reused (reference §1.3). **A stream severed by `GOAWAY`/reset before a final APNs response is mapped to `503 upstream_unavailable` (retryable) and is NOT retried in-process** — see §6.4 "Connection-severed / GOAWAY" for the exact caller-facing rule and the at-most-once rationale.
- Ensure the TLS trust store includes **USERTrust RSA** (SHA-2 root) — already in standard OS/Go pools; only matters if the relay pins or ships a minimal trust store (reference §1.7).

### 6.4 Response / reason mapping → caller-facing error

The relay parses the APNs JSON `{"reason":"…"}` (and the `timestamp` on 410), not just the status code (reference §1.6). It maps to a caller-facing `error.code` and decides retryability. **The relay does not itself retry transient upstream errors across separate caller requests** — it returns a retryable status so the caller's outbox/backoff (in `../01-release-events-and-inbox.md`/`02`) drives retries; the one exception is the in-process JWT-expiry retry (§6.1).

| APNs status | APNs `reason` | Caller HTTP | Caller `error.code` | Retryable (caller)? |
|---|---|---|---|---|
| 200 | — | 200 | — | success |
| 400 | `BadDeviceToken`, `InvalidToken`, `DeviceTokenNotForTopic`, `MissingDeviceToken` | 422 | `bad_device_token` | **No — purge token** (caller disables device, per `02`). `02` treats `BadDeviceToken`/`InvalidToken` equivalently: "Apple has migrated some responses from `BadDeviceToken` to `InvalidToken`; treat them equivalently." See the environment-mismatch caveat below. |
| 400 | `BadTopic`, `MissingTopic`, `TopicDisallowed` | 400 | `bad_topic` | No (config error) |
| 400 | `BadCollapseId` | 400 | `invalid_collapse_id` | No |
| 400 | `BadExpirationDate`, `BadPriority`, `BadMessageId`, `InvalidPushType`, `PayloadEmpty`, `DuplicateHeaders`, `IdleTimeout` | 400 | `bad_request` | No |
| 403 | `ExpiredProviderToken` | (internal) | — | **Relay regenerates JWT, retries once** (§6.1), then maps result |
| 403 | `InvalidProviderToken`, `MissingProviderToken`, `UnrelatedKeyIdInToken`, `BadEnvironmentKeyIdInToken`, `Forbidden`, `BadCertificate*` | 502 | `upstream_auth_error` | No (relay misconfig — **ops alert**, not caller-fixable) |
| 404 | `BadPath` | 502 | `upstream_error` | No |
| 405 | `MethodNotAllowed` | 502 | `upstream_error` | No |
| 410 | `Unregistered`, `ExpiredToken` | 410 | `unregistered` | **No — purge token** (caller disables device). `timestamp` (ms) passed through in `error.message` so caller can compare against its registration time. |
| 413 | `PayloadTooLarge` | 400 | `payload_too_large` | No |
| 429 | `TooManyProviderTokenUpdates` | 503 | `upstream_throttled` | Yes — relay slows JWT refresh; caller backs off |
| 429 | `TooManyRequests` | 429 | `rate_limited` | Yes — per-device backoff; `Retry-After` set |
| 500 | `InternalServerError` | 503 | `upstream_unavailable` | Yes (caller backoff ~15 min) |
| 503 | `ServiceUnavailable`, `Shutdown` | 503 | `upstream_unavailable` | Yes (caller backoff) |

**Default / fallback for unrecognized reasons.** Any **unmapped** `reason` string on a `400`/`403`/`410` (e.g. a future Apple reason the relay does not know) maps to `502 upstream_error`, **non-retryable, no token purge** — a safe default that surfaces the anomaly for investigation rather than silently dropping it or purging a possibly-live token. An unmapped `5xx` maps to `503 upstream_unavailable` (retryable). The exact unmapped reason is recorded in the op-log `upstream_reason` for triage.

**Connection-severed / GOAWAY (no final response).** A request whose HTTP/2 stream is terminated by a `GOAWAY`, connection reset, or any transport drop **before a final APNs response arrives** is mapped to `503 upstream_unavailable` (retryable) — the same as a network timeout. The relay does **NOT** transparently retry such a request in-process; the caller's outbox (`01-release-events-and-inbox.md`) re-dispatches it under the same idempotency key, preserving at-most-once semantics (a request that may have been delivered must not be silently re-sent by the relay). Apple emits `GOAWAY` routinely for maintenance/overload, so this is a normal, expected outcome, distinguished in the op-log/metrics with a `connection_severed` outcome label.

**Environment / sandbox-vs-production mismatch caveat.** A caller that sends `environment=sandbox` with a production token (or vice-versa) gets `BadDeviceToken`/`InvalidToken` from APNs for a token that may be perfectly valid in the other environment. The relay still returns `422 bad_device_token`, but **the caller should NOT treat the first such error from a newly-onboarded device as conclusive grounds to purge** when an environment mismatch is plausible. To let operators distinguish a systemic misconfiguration from genuinely dead tokens, the relay tags these op-logs/metrics so a burst of `bad_device_token` concentrated on **one account** (a likely env mixup) is visible separately from scattered dead tokens across many accounts. See §5.1 (token rules) and §8.4.

Mapping to the caller's own error handling in `02`: `bad_device_token`/`unregistered` → the caller disables the device (`02` "BadDeviceToken / Unregistered: disable the token"); `upstream_throttled`/`rate_limited`/`upstream_unavailable` → the caller retries with backoff. The relay never surfaces `ExpiredProviderToken`/`TooManyProviderTokenUpdates` as caller-facing codes (those are the relay's own JWT lifecycle — `02` notes they apply only to `custom_apns`, never the relay path).

Network timeout / connection failure to APNs → `503 upstream_unavailable` (retryable).

---

## 7. Upstream FCM Client

Built per `./02-apns-fcm-2026-reference.md` §2 and §3.2. Preferred path: `golang.org/x/oauth2/google` `TokenSource` (auto-caches + refreshes) + a custom HTTP/2 client posting the relay-built JSON (reference §3.2 — lighter than the Admin SDK and gives direct control of connection reuse and error classification). The Admin SDK (`firebase.google.com/go/v4/messaging`) is an acceptable alternative; pick exactly one.

### 7.1 OAuth2 service-account token source + caching

- Build a `TokenSource` from the SA JSON with `google.CredentialsFromJSONWithType(ctx, jsonKey, google.ServiceAccount, "https://www.googleapis.com/auth/firebase.messaging")` (least-privilege scope; `CredentialsFromJSON` is deprecated — reference §2.2/§3.2). The `TokenSource` caches and auto-refreshes.
- Access tokens are valid **1 hour**; never mint per request. The library re-mints before expiry; if hand-rolling the JWT-bearer grant, refresh with a ~5-minute safety margin (reference §2.2).
- **Credential resolution:** prefer keyless **Workload Identity / attached SA** when the relay runs on GCP; off-GCP, load the SA JSON from the secret manager (reference §2.2/§4.5; §11 here).
- One token source **per Firebase project**. v1 has one official project (`continuum-prod-android`).

### 7.2 Endpoint

```http
POST https://fcm.googleapis.com/v1/projects/{PROJECT_ID}/messages:send
Authorization: Bearer <oauth2_access_token>
Content-Type: application/json
```

Global host only — **no regional/data-residency send endpoints exist** as of June 2026 (reference §2.3). The host `fcm.googleapis.com` is a **hardcoded constant**, never derived from request input. `{PROJECT_ID}` is the validated `project_id` from the request: it is checked against a **strict charset (`^[a-z0-9-]+$`) before** allowlist lookup and before URL interpolation, so an allowlist row can never inject path-traversal or override the host (§11 egress/SSRF posture).

### 7.3 Data-only message construction

As in §5.2: `message.token` set to the caller's `token`; `message.data` is string-valued only (a fixed, closed key set — §5.2/§12); `message.android.priority` is the canonical `"HIGH"`/`"NORMAL"` enum by mode; the caller's `collapse_key` is mapped to the REST field `message.android.collapseKey` (camelCase) when present; **no `notification` block**. The relay can optionally use `validateOnly: true` (top-level, camelCase) for the `relayctl`-driven test path (reference §2.5) but **never** on production sends.

### 7.4 Error mapping → caller-facing error + Retry-After

The relay classifies on `error.status` / the `FcmError.errorCode` in `details[]`, not the bare HTTP code (reference §2.6).

| FCM HTTP | `error.status` | Caller HTTP | Caller `error.code` | Retryable (caller)? |
|---|---|---|---|---|
| 200 | — | 200 | — | success |
| 400 | `INVALID_ARGUMENT` | 422 | `invalid_argument` | No. Ambiguous (bad payload **or** bad token); since the relay payload is independently valid, the caller may treat it as a dead token (`03` / reference §2.6). |
| 401 | `THIRD_PARTY_AUTH_ERROR` | 502 | `upstream_error` | **Not expected on this pure-Android path** — see note below. No auto-purge; surface for investigation. Do **NOT** page as a relay-credential failure. |
| 403 | `SENDER_ID_MISMATCH` | 403 | `sender_id_mismatch` | **No — disable device** (token minted under a different project; `03`). |
| 404 | `UNREGISTERED` | 410 | `unregistered` | **No — purge token** (`03`). Caller-facing **410** is an **intentional cross-provider normalization** — see note below. |
| 429 | `QUOTA_EXCEEDED` (RESOURCE_EXHAUSTED) | 429 | `rate_limited` | Yes — **honor `Retry-After`** if present; else default **60 s** (reference §2.7). |
| 429 / 400 | per-device rate (`DeviceMessageRateExceeded` — see note) | 429 | `rate_limited` | Yes — **retryable; NOT a token purge**. Conservative default `Retry-After` (60 s) if none provided. |
| 401 / 403 | (HTTP **status**, not `FcmError` code) `UNAUTHENTICATED` / `PERMISSION_DENIED` on `messages:send`, **or** a non-200 from `oauth2.googleapis.com/token` at mint time | 502 | `upstream_auth_error` | No — **relay's own OAuth2 / service-account credential failure** (**ops alert**). See note below. |
| 500 | `INTERNAL` | 503 | `upstream_unavailable` | Yes (backoff; no documented `Retry-After`). |
| 503 | `UNAVAILABLE` | 503 | `upstream_unavailable` | Yes — **honor `Retry-After`** if present (reference §2.7). |
| — | `UNSPECIFIED_ERROR` / any unmapped `errorCode` | 502 | `upstream_error` | No auto-purge; surface for investigation (reference §2.6). |

**Note — `THIRD_PARTY_AUTH_ERROR` is not a relay-credential signal on this path.** Per Firebase's error-codes page, `THIRD_PARTY_AUTH_ERROR` means *"APNs certificate or web-push auth key was invalid or missing"* and is returned **only** for messages targeting **iOS-via-APNs or Web Push** registration tokens — i.e. when FCM itself must authenticate downstream to Apple/VAPID. The relay's `/v1/fcm/send` sends **only Android registration tokens** (data-only `message.token`, package `com.continuum.app.android`), so a legitimate Android send can never produce `THIRD_PARTY_AUTH_ERROR`. If it ever appears it indicates a **per-token/registration anomaly** (an Android token that is somehow APNs/web-bound — a client/registration bug), not a relay `.p8`/SA misconfiguration; treat it as `502 upstream_error`, surface for investigation, and do **not** page on-call as a credential failure.

**Note — the relay's OWN FCM credential failure surfaces differently.** A failure of the relay's OAuth2 service-account auth shows up as a non-200 from the Google token endpoint (`oauth2.googleapis.com/token`) at mint time, or as an HTTP **status** `401 UNAUTHENTICATED` / `403 PERMISSION_DENIED` on `messages:send` (the gRPC/REST status, **not** an `FcmError.errorCode`). **These** are the signals that map to `upstream_auth_error` and warrant an ops alert (the alerting condition in `01-implementation-plan.md` keys on this `upstream_reason`, not on `THIRD_PARTY_AUTH_ERROR`).

**Note — `UNREGISTERED` is deliberately re-mapped from FCM's native 404 to caller-facing 410.** FCM natively returns `UNREGISTERED` as HTTP **404**; the relay normalizes it to caller-facing **410** so it matches the same "dead token, purge" semantic the APNs path uses for `Unregistered` (where APNs natively uses 410). This is an **intentional cross-provider normalization** (not a bug) so the caller's device-disable logic is uniform across APNs and FCM. Similarly `SENDER_ID_MISMATCH` keeps FCM's native 403.

**Note — per-device rate exceedance.** FCM enforces per-device Android caps (~240 msg/min, ~5000 msg/hr) that surface as a `DeviceMessageRateExceeded`-class error, **distinct from project-level `QUOTA_EXCEEDED`** (reference §2.7). Because the relay fans out bursty per-device wakes, this is a plausible real outcome. The exact error code/string for per-device exceedance is **not firmly documented**, so the relay detects it **defensively** by classifying on `error.status`/`errorCode` and maps it to a **retryable `429 rate_limited`** with a conservative default `Retry-After` — **never a token purge** (the device is fine, just rate-limited). The relay's coarse per-`(account, token_hash)` limiter (§9.1) is the local guard, but an upstream per-device error must still map to `429`, not fall through to `502`.

**Retry-After honoring (reference §2.7):** on FCM `429`, the relay reads the upstream `Retry-After`; if absent it defaults to 60 s. It forwards this as the caller-facing `Retry-After` header on the `429 rate_limited` response (relative `delay-seconds`, not an absolute epoch — reference §4.2). On `503 UNAVAILABLE` it forwards any upstream `Retry-After`.

Mapping to `03` caller handling: `unregistered`/`sender_id_mismatch`/`invalid_argument` → caller disables the device; `rate_limited` → caller backs off and may fall back to `priority: NORMAL` after repeated 429s (`03` open question, resolved "yes with cooldown"); `upstream_auth_error` → caller surfaces admin diagnostics, no retry. Note that `upstream_auth_error` is now sourced from the **relay's own** OAuth2/SA credential signals (HTTP `401 UNAUTHENTICATED` / `403 PERMISSION_DENIED` on `messages:send`, or a token-mint failure), **not** from `THIRD_PARTY_AUTH_ERROR` — which on this Android-only path is a registration anomaly, not a relay-credential problem (see the notes above and §7.5).

### 7.5 Reconciliations with the older `03` contract

- **`Retry-After` on 429.** `03` says "honoring the `Retry-After` response header if present (FCM uses standard HTTP semantics here)". The reference (§2.6/§2.7) refines this: FCM's error page promises only `google.rpc.QuotaFailure` (not `RetryInfo`); treat `Retry-After`/`RetryInfo` as optional and fall back to 60 s. The relay implements the **reference's** refinement while still surfacing a `Retry-After` to the caller exactly as `03` expects.
- **No batch endpoint.** The reference (§2.1) confirms FCM's batch endpoint is discontinued; the relay sends **one POST per request** (it already does — one caller request = one device).
- **Upstream field casing — `collapseKey`, `priority`, `validateOnly`.** `03` writes the upstream Android fields in snake_case (`collapse_key`) and lowercase priority (`high`/`normal`), and writes the dry-run flag as `validate_only`. The correct **FCM HTTP v1 REST** field names are `android.collapseKey` (camelCase), the canonical `priority` enum `"HIGH"`/`"NORMAL"` (uppercase; lowercase also accepted), and top-level `validateOnly` (camelCase) (reference §2.4/§2.5). The relay uses the **REST-correct names on the upstream wire** while leaving the **inbound caller field `collapse_key` unchanged** per `03`. FCM v1 rejects unknown fields with `INVALID_ARGUMENT`, so this reconciliation is mandatory, not cosmetic.
- **FCM 4-distinct-collapse-key-per-device cap.** FCM stores at most **4 distinct collapse keys per device while offline**; a 5th distinct key evicts one nondeterministically (reference §2.7 — a delivery/storage behavior, **not** a per-message validation limit). Because `collapse_key` is a per-series HMAC and a device may follow **more than 4 series**, collapsible coalescing is **best-effort**: excess distinct keys evict nondeterministically and a collapsible wake may be dropped. This is **acceptable** because the durable inbox + sync (`01-release-events-and-inbox.md`) recovers any dropped wake; the relay treats `collapse_key` opaquely and takes no action. **Asymmetry note:** APNs has no equivalent documented per-device `collapse-id` cap, so the FCM path alone carries this best-effort caveat.
- **External contract unchanged.** None of the above alters the `03` request/response shapes; only the relay's internal upstream behavior is specified more precisely.

---

## 8. Account, Key and Allowlist Model

### 8.1 DB schema (PostgreSQL)

Goose-style migration. All IDs are ULIDs stored as `text` for consistency with the Silo convention; timestamps are `timestamptz`.

```sql
-- relay_accounts: one per opt-in self-hosted installation.
CREATE TABLE relay_accounts (
    id          text        PRIMARY KEY,                 -- ULID
    name        text        NOT NULL,                    -- operator-facing label
    status      text        NOT NULL DEFAULT 'active'
                CHECK (status IN ('active','disabled')),
    note        text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- relay_api_keys: revocable bearer keys (rk_ prefix). Secret never stored.
CREATE TABLE relay_api_keys (
    id            text        PRIMARY KEY,               -- ULID
    account_id    text        NOT NULL REFERENCES relay_accounts(id) ON DELETE CASCADE,
    key_prefix    text        NOT NULL,                  -- non-secret, indexed lookup handle (e.g. rk_live_3Qw9XYZ)
    key_hash      bytea       NOT NULL,                  -- HMAC-SHA256(pepper, secret), 32 bytes
    env           text        NOT NULL DEFAULT 'live'
                  CHECK (env IN ('live','test')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,                           -- throttled write (<= 1/min)
    expires_at    timestamptz,                           -- optional
    revoked_at    timestamptz                            -- non-null = revoked
);
CREATE UNIQUE INDEX relay_api_keys_prefix_uidx ON relay_api_keys (key_prefix);
CREATE INDEX        relay_api_keys_account_idx  ON relay_api_keys (account_id);

-- relay_apns_allowlist: which APNs topics an account may push to.
CREATE TABLE relay_apns_allowlist (
    account_id  text        NOT NULL REFERENCES relay_accounts(id) ON DELETE CASCADE,
    topic       text        NOT NULL,                    -- e.g. com.continuum.app.ios
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, topic)
);

-- relay_fcm_allowlist: which (project, package) pairs an account may push to.
CREATE TABLE relay_fcm_allowlist (
    account_id    text        NOT NULL REFERENCES relay_accounts(id) ON DELETE CASCADE,
    project_id    text        NOT NULL,                  -- e.g. continuum-prod-android
    package_name  text        NOT NULL,                  -- e.g. com.continuum.app.android
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, project_id, package_name)
);

-- relay_op_logs: redacted operational/audit log. No tokens, no content.
CREATE TABLE relay_op_logs (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    account_id  text        REFERENCES relay_accounts(id) ON DELETE SET NULL,
    request_id  text,                                    -- ULID echoed to caller
    event       text        NOT NULL,                    -- e.g. send.apns, send.fcm, auth.reject, admin.key_issue
    provider    text,                                    -- 'apns' | 'fcm' | null
    outcome     text,                                    -- 'accepted' | 'rejected' | 'upstream_error' | ...
    status_code int,                                     -- caller-facing HTTP status
    error_code  text,                                    -- caller-facing error.code
    upstream_reason text,                                -- APNs reason / FCM status (non-sensitive)
    token_hash  text,                                    -- SHA-256(token) hex, NOT the raw token
    egress_ip   inet,                                    -- caller egress IP
    latency_ms  int
);
CREATE INDEX relay_op_logs_account_time_idx ON relay_op_logs (account_id, occurred_at DESC);
CREATE INDEX relay_op_logs_time_idx         ON relay_op_logs (occurred_at DESC);
```

Notes:
- `relay_op_logs` is the only table written on the hot path besides the throttled `last_used_at`. It carries **no raw token, no payload, no identity** — only a `token_hash` (SHA-256 hex) for correlating repeated failures to one device without storing the token (reference §4.6). Retention: see §13.
- No table stores APNs/FCM device tokens, notification content, user/profile IDs, or token aliases — by design (`00` "Its database holds only…").

### 8.2 API-key format, hashing, lookup, compare

- **Format:** `rk_<env>_<random>`. The **prefix** = `rk_<env>_<first 6–8 chars of the random>` is **non-secret**, stored in cleartext, and uniquely indexed (`key_prefix`). The full random part is 32 bytes from `crypto/rand`, base62/base64url-encoded. GitHub/Stripe-style design — lets a request `SELECT` by indexed prefix in O(1), then constant-time-compare (reference §4.1).
- **Hashing:** store `HMAC-SHA256(pepper, secret)` (32 bytes) where `pepper` is a server-side secret in the secret manager. A fast keyed hash is appropriate for **high-entropy 256-bit random keys** — OWASP's slow-KDF (argon2id) guidance is scoped to low-entropy passwords and would needlessly throttle per-request auth (reference §4.1). Never store plaintext.
- **Lookup + compare on each request (constant-time regardless of whether the prefix exists):**
  1. Parse `Authorization: Bearer rk_<env>_<rest>`; reject malformed → `401 unauthorized`.
  2. Derive `key_prefix` from the token; `SELECT … FROM relay_api_keys WHERE key_prefix = $1` (unique index).
  3. If no row, or `revoked_at IS NOT NULL`, or `expires_at < now()`, or the account is `disabled` → still perform a **dummy `HMAC-SHA256` against a fixed decoy hash with `crypto/subtle.ConstantTimeCompare`**, then return the uniform `401 unauthorized` (do not distinguish reasons to the caller). **Do not short-circuit before the HMAC+compare** — an unknown prefix must take the same time as a known prefix with a wrong secret, or the timing difference is a key-enumeration oracle (a known/live prefix would otherwise reject measurably faster). The fine-grained `reason` (`unknown_prefix`/`hash_mismatch`/…) is kept **only in internal logs and the internal-only `/metrics`** (§13.2), never surfaced to a tenant or an untrusted scraper.
  4. Compute `HMAC-SHA256(pepper, presented_secret)` and `crypto/subtle.ConstantTimeCompare` against `key_hash` (or the decoy). Mismatch → `401`. Never use `==`/`bytes.Equal` on secret material (reference §4.1).
  5. Throttle-update `last_used_at` (at most once per minute per key). **The throttle clock lives in Redis** (`lastused:{key_prefix}` set `NX EX 60`) so the "≤1/min/key" bound holds **cross-replica** (a per-instance clock would allow N writes/min/key across N replicas); only when that `NX` succeeds does the relay issue the Postgres `UPDATE`. The Redis key carries no secret. This avoids a Postgres write per request (reference §4.1) without leaking a per-key validity side channel through DB write cadence.
- **Rotation:** multiple live keys per account are supported (issue a new key, deploy it on the server, then revoke the old) for zero-downtime rotation (reference §4.1).
- **Client-IP resolution (trusted-proxy requirement).** The relay runs **behind a TLS load balancer** (§4.2, §14.1), so Go's `http.Request.RemoteAddr` is the LB/proxy address, not the caller. The relay derives the client IP as follows: **use `RemoteAddr` unless the immediate peer is in the allowlisted trusted-LB CIDR set, in which case take exactly the right-most untrusted hop from the LB-set forwarding header (`X-Forwarded-For`)**. **Caller-supplied forwarding headers are ignored** — a caller cannot set `X-Forwarded-For`/`X-Real-IP` to forge an IP. The trusted-proxy CIDR list is a **config value**. Both the per-IP auth-failure limiter and the op-log `egress_ip` consume this single resolved IP. (Without this, a caller could either spread brute-force across unlimited fake IPs to defeat the per-IP throttle (OWASP API2:2023) and poison `egress_ip`, or — if `RemoteAddr` were trusted blindly behind the LB — collapse every tenant onto one IP and trip the throttle for all at once.)
- **Anti-brute-force — per-IP auth-failure limiter (specified).** Auth failures are rate-limited **per resolved client IP** (independent of the per-account bucket) to blunt key-guessing (OWASP API2:2023, reference §4.1). Concretely:
  - **Store:** Redis, key `authfail:{client_ip}`, sliding/fixed window with `EXPIRE`.
  - **Window / threshold:** e.g. **20 failures / 5 min** per IP (tunable).
  - **Response on breach — *soft* lockout, not a hard block:** added latency / a `429` with `Retry-After` (e.g. 60 s backoff) rather than dropping the connection. This is deliberate because the doc anticipates **VPN/CGNAT shared egress** (§3.4): multiple self-hosted servers can share one egress IP, so a hard per-IP block would be collateral damage from a noisy/compromised neighbor. Optionally scope tracking by `(key_prefix, IP)` to further isolate neighbors.
  - **Fail-open/closed stance:** this limiter **fails open** if Redis is unreachable (availability over a perfectly-enforced anti-guessing cap), logging `authfail_limiter.degraded`. The hard auth boundary (HMAC compare, allowlist) is always enforced from Postgres regardless.
  - **Metric + runbook:** `relay_auth_failures_total{reason=…}` (internal only) plus a documented response runbook for the key-guessing alert (R8, §6.4 monitoring): inspect the offending IP/prefix, confirm whether a single account or a guessing pattern, revoke/rotate as needed.

### 8.3 The `rk_` token is shown exactly once

`relayctl key issue` prints the full `rk_…` token to stdout **once**; only the HMAC hash and prefix are persisted. There is no endpoint or command that can recover a secret. Lost keys are revoked and re-issued.

### 8.4 Allowlist enforcement

Enforced **after** auth, **before** building the upstream payload:

- **APNs (`/v1/apple/send`):** the request `topic` must exist in `relay_apns_allowlist` for the caller's `account_id`. Not present → `403 topic_not_allowed`. The official build config allowlist (allowlist config, **not** part of the contract) is the set `com.continuum.app.ios`, `com.continuum.app.tvos`, `com.continuum.app.macos` (`02` resolved-questions: per-platform topics).
- **FCM (`/v1/fcm/send`):** the `(project_id, package_name)` pair must exist in `relay_fcm_allowlist` for the caller's `account_id`. Missing project → `403 project_not_allowed`; project present but package not → `403 package_not_allowed`. Official build config: project `continuum-prod-android`, package `com.continuum.app.android`.
- Allowlists are loaded per request from Postgres (cheap, small) or from a short-TTL in-memory cache keyed by `account_id` (invalidated on `relayctl allowlist … set`). The cache must be per-account-scoped so one account's allowlist can never authorize another's push.

These are **allowlist config values**, set by `relayctl`, not part of the external wire contract — a different deployment (e.g., a fork) would set different topics/packages.

> **Environment ⇄ token consistency (operational caveat).** The allowlist does **not** (and cannot) verify that the request `environment` matches the environment the device `token` was minted for. A `sandbox`-vs-`production` mixup presents downstream as APNs `BadDeviceToken`/`InvalidToken` → `422 bad_device_token` (§6.4), which would otherwise tell the caller to purge a perfectly good token. To let operators catch a **systemic** misconfiguration, the relay emits a distinct op-log/metric tag for these so a burst of `bad_device_token` concentrated on **one account** (likely an env mixup) is distinguishable from scattered genuinely-dead tokens across many accounts. The cross-field rule is documented in §5.1 and §6.4.

---

## 9. Rate Limiting and Quotas

### 9.1 Model

Per-account **token bucket** enforced in Redis with an atomic Lua script (refill → check → consume in one `EVAL`), state stored as a HASH `rl:{account_id} = {tokens, last_refill_ts}` with a TTL for idle-account eviction (reference §4.2). A Redis-backed limiter (not per-instance `x/time/rate`) is required because the relay runs multiple replicas behind a load balancer — per-instance limiters would not enforce a global cap.

A second, coarser limiter keys on `(account_id, token_hash)` to blunt hammering a single device (the `02`/`03` "rate limited by API key and coarse token hash" requirement).

**Redis operational requirements (eviction, HA, sizing).** Redis backs both idempotency (fail-closed) and rate limiting (fail-open), so its configuration is correctness-relevant, not just performance:

- **Non-evicting policy for idempotency.** The idempotency keyspace **must** run under `maxmemory-policy noeviction` (or live in a **dedicated Redis/db** separate from the rate-limit keyspace) so that memory pressure can **never silently evict an in-flight idempotency lock** — an evicted lock would silently violate at-most-once (a concurrent retry would get `NX`-success and double-send). Rate-limit keys may be evicted harmlessly; idempotency keys must not. This is called out in risks R4/R5.
- **HA topology.** Run Redis with an HA topology (Sentinel or Cluster) with prompt failover. A Redis failover/restart makes **every** send `503` while idempotency fails closed (§10) — a push-delaying, not push-losing, event (the caller outbox retries), **but only as long as idempotency stays fail-closed**. A Redis outage should **page ops**.
- **Keyspace sizing.** Size the idempotency keyspace from the quota math: ~`50k/day/account × N accounts`, each entry holding `{status, body, upstream_id, canonical_hash}` under a ~24 h TTL. Provision `maxmemory` with headroom above that working set so `noeviction` never starts rejecting writes.

### 9.2 Suggested limits (ties to `00` open question #1)

| Limit | Default | Source / rationale |
|---|---|---|
| Per-account burst | **~10 req/s** | `00` open question #1 ("Suggest 10 req/sec burst") |
| Per-account daily quota | **50,000 req/day** | `00` open question #1 ("50,000 req/day initial quota") |
| Bucket refill rate | 10 tokens/s, burst capacity 20 | derived from the 10 req/s target with a small burst allowance |
| Per-(account, token) coarse cap | e.g. 1 req/3 s sustained | abuse guard on a single device |

These are tunable per account (future column on `relay_accounts`); v1 ships a single global default and tunes from logs (`00`). The Silo server already paces its calls client-side (default 5 req/s shared token bucket — `02`/`03` "Relay pacing"), so a well-behaved caller stays comfortably under the 10 req/s burst; the relay limit is the backstop against a misbehaving or compromised caller (OWASP API4:2023 — cap per-tenant downstream spend).

**Daily-quota mechanics (store, reset, fail-open).** The per-second token bucket (§9.1) does **not** model the 50,000 req/day cap; the daily counter is a **separate Redis counter** keyed `rl:day:{account_id}:{yyyymmdd}` with an `EXPIRE` past the day boundary, incremented atomically per accepted send:

- **Reset semantics:** a **fixed UTC calendar day** (the `{yyyymmdd}` is UTC), not a rolling 24 h — simple, predictable for operators, and the key self-expires.
- **Breach:** when the counter reaches 50,000 the account's sends return `429 rate_limited` for the rest of the UTC day with a `Retry-After` = seconds until 00:00 UTC. Daily exhaustion blocks **all** of an account's pushes until reset — a notable availability event, so it is **alerted** (see below), not silent.
- **Fail-open during a Redis outage:** like the per-second bucket, the daily counter **fails open** if Redis is unreachable — but this is exactly the abuse-amplification window, so it is **bounded** by the in-process fallback limiter in §9.4 (a compromised key cannot burn unlimited upstream sends during the outage) and the outage pages ops. The daily quota **cannot be enforced cross-replica while Redis is down**; that limitation is explicit.
- **Alerting:** a metric/alert fires for accounts **approaching** (e.g. ≥80%) or **hitting** the daily cap, since a legitimate hundreds-of-users server can plausibly hit 50k/day on a big release burst — operators should see this and raise the cap rather than have the account silently throttle. Reconcile the 50k/day default against the `02`/`03` burst-cap math when tuning.

### 9.3 429 response

On limit exceeded the relay returns:

```http
HTTP/2 429
Retry-After: 3
Content-Type: application/json
X-Request-Id: 01JRELAY...

{ "error": { "code": "rate_limited",
             "message": "account rate limit exceeded",
             "request_id": "01JRELAY..." } }
```

`Retry-After` is a **relative `delay-seconds`** integer (never an absolute Unix epoch — reference §4.2), computed from the bucket's time-to-next-token. The daily-quota breach also returns `429 rate_limited` with a longer `Retry-After` (seconds until the daily window rolls).

### 9.4 Fail-open vs fail-closed (bounded, not unlimited)

If Redis is unreachable, the rate-limit middleware **fails open** for availability (a push is more valuable than a perfectly enforced cap), but **bounded** — it does **not** simply remove the cap. When Redis is down the relay falls back to a **conservative per-instance in-process limiter** (`golang.org/x/time/rate`, keyed per `account_id`) that caps each account to a small multiple of its normal rate. This matters because the per-account quota is the primary control bounding a **stolen-key** attacker's spend and per-tenant downstream APNs/FCM cost (OWASP API4:2023, §3.5 "Stolen relay API key", G6): an unbounded fail-open would let a compromised `rk_` key send without limit against the official Apple/Google credentials — including burning the shared FCM project quota (§9.5) common to all tenants — for the entire outage. The **allowlist (always enforced from Postgres)** remains the hard boundary. A `rate_limit.degraded` op-log + metric fires; the **daily quota cannot be enforced cross-replica** during the outage, and an outage **pages ops**. The idempotency layer (§10), by contrast, **fails closed** to avoid double-sends. (Reference §4.2 — explicitly a per-endpoint decision.)

### 9.5 Shared FCM project quota — global limiter / circuit breaker

The per-account limits above are **local to each account**, but the **FCM project quota (~600k messages/min, reference §2.7) is SHARED across ALL relay accounts** sending to `continuum-prod-android`. Without a relay-side guard, one noisy/compromised account can exhaust the shared project quota and every other account's sends start returning `429 QUOTA_EXCEEDED` — a **multi-tenant fairness** failure (one noisy tenant degrades all). The relay therefore adds a **global (per-project) outbound limiter / circuit breaker** at the aggregation point — the only place that *can* see aggregate FCM rate across accounts:

- **(a) Track aggregate FCM send rate** against the documented project quota (a Redis counter keyed per project, e.g. `fcmq:{project_id}:{yyyymmddhhmm}` with a per-minute window).
- **(b) Shed load before hammering FCM:** when near the quota, return `503 upstream_throttled` (retryable) **before** forwarding, rather than spending quota on calls that FCM will reject anyway (some errors still count against quota).
- **(c) Anti-spike smoothing (optional):** honor the reference §2.7 guidance to avoid concentrating traffic in the 2-minute windows around `:00/:15/:30/:45` — the relay, as the cross-account aggregation point, is the only component that can actually implement this ramp.
- **Observability:** a `relay_fcm_project_quota_headroom` gauge plus an alert when headroom is low. This is explicitly a **multi-tenant fairness** control (a single account must not exhaust the shared project quota for all).

---

## 10. Idempotency

### 10.1 Key format

`Idempotency-Key: <notification_delivery_id>:<push_device_id>:<attempt_number>` (`02`/`03`). This is **scoped per account** in storage (so two accounts cannot collide). The relay validates the header is present and non-empty on send endpoints; missing → `400 missing_idempotency_key`. The three-part shape is not strictly parsed (it is opaque to the relay), but it is length-limited (≤ 255 chars, Stripe-style — reference §4.3).

### 10.2 Store, TTL, locking, duplicate semantics

Redis, namespaced `idem:{account_id}:{key}` (reference §4.3):

1. `SET idem:{account}:{key} <in_flight_marker> NX EX <lock_ttl>` (`lock_ttl` = 30 s). The `<in_flight_marker>` carries a **unique per-attempt nonce** so the completing request can prove ownership (see invariant below).
2. **NX success** → this is the first request. Process it; on completion, **compare-and-set**: overwrite the key with the serialized `{status, body, upstream_id, canonical_hash}` and a **~24 h TTL** **only if the stored value is still this request's own in-flight marker** (i.e. the lock was not stolen by a concurrent request after a TTL expiry). Success *and* error are both recorded (reference §4.3 / Stripe model).
3. **NX fail + stored completed result** → **replay** it: return the original status code and body verbatim (so a retried send returns the same `apns_id`/`fcm_message_name`, never a second upstream send).
4. **NX fail + still the in-flight marker** → the original is mid-flight → **`409 conflict`** (`{"error":{"code":"idempotency_conflict",…}}`) so the caller retries later.
5. **Same key, different request payload** → **`422 unprocessable`** (`idempotency_key_reuse`). The relay stores a `canonical_hash` of the request alongside the result and compares (reference §4.3). **The canonical hash is computed over the validated fields with the device `token` replaced by its already-computed `token_hash` (SHA-256) — the raw token is NEVER part of the canonical form.** The stored idempotency value contains only `{status, body, upstream_id, canonical_hash}`; it holds **no raw token** (matching the op-log guarantee §12, NG1). This keeps the "no token aliases / stateless v1" privacy guarantee intact even though the value lives in Redis under `idem:{account}:{key}` for ~24 h.

> **Lock-TTL invariant (no double-send, no result-clobber).** The upstream APNs/FCM call **must run under a hard context deadline strictly shorter than `lock_ttl`** — e.g. **upstream timeout 10 s, `lock_ttl` 30 s** — and the handler **must refuse to send upstream if its own deadline would exceed the lock TTL**. Otherwise a slow first attempt (HTTP/2 setup + JWT/OAuth refresh + GOAWAY-reconnect can stack up) could outlive the 30 s lock; the lock would expire mid-flight, a concurrent retry of the same `Idempotency-Key` would get `NX`-success and fire a **second** upstream send (defeating at-most-once), and when the slow first attempt finally completed it would clobber the stored result with a different `apns_id`. The **compare-and-set on completion** (step 2) prevents the clobber — a lock that was stolen by a concurrent request cannot have its result overwritten by the original — and the **deadline-shorter-than-TTL** rule prevents the double-send in the first place. (Optionally, a lock heartbeat may renew the marker while genuinely in flight, but the deadline rule is the primary guarantee.) This invariant is the whole reason the idempotency store exists (R4).

This guarantees **at-most-once upstream send** under the caller's concurrent retries (the `../01-release-events-and-inbox.md` outbox can re-claim and re-dispatch the same `(delivery_id, device_id, attempt_number)`), which is exactly why the key includes `attempt_number`: attempt 2 is a *new* idempotency key (intended re-send after a failure), while two concurrent dispatches of attempt 1 collapse to one upstream call.

### 10.3 What a duplicate returns (summary)

| Situation | Response |
|---|---|
| First request | Process; record `{status, body, upstream_id, canonical_hash}` (no raw token — §10.2) for ~24 h. |
| Duplicate, original completed | Replay stored status + body (same `apns_id`/`fcm_message_name`). |
| Duplicate, original in-flight | `409 idempotency_conflict`. |
| Same key, different payload | `422 idempotency_key_reuse`. |
| Missing key | `400 missing_idempotency_key`. |

Idempotency **fails closed**: if Redis is unavailable, send endpoints return `503 upstream_unavailable` (retryable) rather than risk a double-send (contrast §9.4).

---

## 11. Security Requirements

- **TLS only.** Public endpoints are HTTPS over HTTP/2; no plaintext listener. TLS 1.2+ (prefer 1.3). HSTS on the public hostname. (`02`/`03` "Relay API uses TLS only".)
- **Secret management.** APNs `.p8`, FCM SA JSON, the API-key HMAC pepper, and DB/Redis credentials live in a **secret manager** (KMS/Vault/cloud secret-manager). Ranking: secret-manager > tightly-permissioned mounted file > env var (**discouraged**) > in-image/repo (**forbidden**) (reference §4.5). On GCP, prefer keyless Workload Identity for FCM and avoid a downloadable SA JSON entirely (reference §4.5). Never commit or bake credentials into the image.
- **Key storage.** API keys stored only as `HMAC-SHA256(pepper, secret)` + non-secret prefix; constant-time compare; revocable; optional expiry; multiple live keys per account for rotation (§8.2). The relay's own `rk_` secret is shown once and never recoverable.
- **No raw secrets in transit out.** No endpoint returns the `.p8`, SA JSON, pepper, or any `rk_` secret. `/metrics` and op-logs carry none.
- **Redaction (enforced in middleware so it cannot be bypassed).** Never log: `Authorization`/bearer header, raw API key (log only the non-secret prefix or a hash), raw APNs/FCM device tokens (log `SHA-256` truncated hash only), notification payload bodies, the relay JWT, or OAuth2 access tokens (reference §4.6; `02`/`03` privacy/security requirements). See §12/§13.
- **Isolation / multi-tenancy.** Every API key, rate-limit bucket, idempotency key, allowlist cache entry, and log line is scoped by `account_id`. Shared-store keys are namespaced (`idem:{account}:…`, `rl:{account}`) to prevent cross-tenant collision or replay (reference §4.7). For APNs, connection pools are per-team (§6.1).
- **Network posture.** `/metrics` and `/readyz` bound to the internal/scrape network; only `/v1/*` and `/healthz` are public. `relayctl` reaches Postgres over a private network with a privileged DSN held by operators only — there is no public admin API (§5.6).
- **Input hardening.** `DisallowUnknownFields`, body size cap (e.g. 16 KiB — far above the ~1 KB legitimate body), explicit server timeouts (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes`) since the zero value means no timeout (reference §3.3). Graceful shutdown via `srv.Shutdown(ctx)` on SIGTERM.
- **Egress / SSRF posture.** Both upstream hosts are **hardcoded constants** — `api.push.apple.com` / `api.sandbox.push.apple.com` (APNs) and `fcm.googleapis.com` (FCM) — **never derived from any request field**. The only request-derived value interpolated into an upstream URL is the FCM `project_id` (into `/v1/projects/{project_id}/messages:send`); it is **allowlist-checked** (§8.4) **and** validated against a strict charset **before** allowlist lookup and URL construction: `project_id` and `package_name` against `^[a-z0-9-]+$` / reverse-DNS, `topic` against the APNs reverse-DNS shape. This ensures an allowlist row can never introduce a path-traversal/host-override and the host can never be templated from caller input (SSRF/path-injection prevention).
- **Anti-brute-force** on auth failures per resolved client IP, with trusted-proxy IP resolution (§8.2).

---

## 12. Privacy Requirements

The relay's privacy guarantee is **structural**, not policy-based: the request schema has no field that can carry content, and unknown fields are rejected.

- **Content-free guarantee.** The send-endpoint structs contain only the fields in §5.1/§5.2. There is **no** title, body, image URL, media ID, username, server hostname, server URL, library/collection/item name, or watched-state field (`02`/`03` privacy requirements). The relay-built payloads use only generic localization keys (`SILO_NOTIFICATION_TITLE` / `SILO_NOTIFICATION_GENERIC_BODY`) and opaque IDs.
- **Reject any unexpected field.** JSON decoding uses `DisallowUnknownFields`; any field not in the schema → `400 unexpected_field`. This is the enforcement point for the content-free guarantee — a caller (buggy or hostile) cannot smuggle content through an extra key.
- **Badge disabled by default.** `badge` is omitted unless explicitly provided, so unread counts don't leak through APNs/FCM by default (`02`/`03`).
- **FCM is data-only with a closed key set.** No top-level `notification` block is ever constructed, so Google carries no rendering content (`03`). The `message.data` map is a **fixed, closed set of keys** — exactly `{v, wake, server_device_id, delivery_id}` — with **no caller-controlled key or value** beyond the already-opaque IDs (§5.2). `badge` is **never** injected into `data` (it has no data-only home and is ignored on the FCM path — §5.2), so the data-map allowlist cannot be widened by a provided badge or any extra key.
- **No raw token in any store.** Neither the op-log nor the idempotency store holds a raw device token: the op-log carries only `token_hash` (SHA-256), and the idempotency value's `canonical_hash` is computed with the token replaced by `token_hash` (§10.2). The stateless-v1 "no token aliases" guarantee (NG1) holds across both stores.
- **Redacted logging.** Op-logs and structured logs **never** contain: raw device tokens (only `SHA-256` hash), notification content (none exists), the `Authorization` header, the relay JWT, OAuth2 tokens, or the raw API key (only the prefix). Full request payloads are never persisted (`02`/`03` "Relay logs do not persist full request payloads"). What **is** logged for triage: `request_id`, `account_id`, status code, caller `error.code`, upstream reason, `token_hash`, egress IP, latency (reference §4.6).
- **No identity storage.** The relay stores no user/profile identity, no device→user mapping, no server URL, and no token aliases (`00`).

---

## 13. Observability

### 13.1 Structured logs (with redaction)

`log/slog` JSON handler (reference §3.5). Every request emits one structured line through a redaction middleware that strips the disallowed fields (§12). Example (a rejected, allowlist-failed APNs send):

```json
{
  "time": "2026-06-13T10:00:00Z",
  "level": "WARN",
  "msg": "send.apns rejected",
  "request_id": "01JRELAY...",
  "account_id": "01JACCOUNT...",
  "provider": "apns",
  "event": "send.apns",
  "outcome": "rejected",
  "status_code": 403,
  "error_code": "topic_not_allowed",
  "token_hash": "9f86d081...c2b5",
  "egress_ip": "203.0.113.7",
  "latency_ms": 2
}
```

Redaction is implemented in middleware so no code path can log a raw token, the bearer header, the JWT, or an OAuth2 token. The redacted audit subset also lands in `relay_op_logs` (§8.1) for queryable history. **Op-log retention:** successful sends **>14 days** are deleted; failures/upstream errors **>90 days** are deleted (mirrors the `02`/`03` `push_delivery_attempts` retention guidance). At 50k/day/account this table grows unbounded without active GC, so retention is a **shipped deliverable, not a deferral** — enforced by a scheduled sweeper / `relayctl logs --gc` (specified in `01-implementation-plan.md` Phase 5), with a disk-usage / table-growth alert. Time-partitioning of `relay_op_logs` is the recommended store strategy; see §15 #8.

### 13.2 Metrics catalog (Prometheus, `/metrics`)

`prometheus/client_golang` (reference §3.5). Labels avoid high-cardinality fields (no token, no `request_id`).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `relay_requests_total` | counter | `provider`, `endpoint`, `status_code`, `error_code` | All inbound send requests by outcome. |
| `relay_request_duration_seconds` | histogram | `provider`, `endpoint` | End-to-end relay latency. |
| `relay_upstream_requests_total` | counter | `provider`, `upstream_status`, `upstream_reason` | APNs/FCM calls by upstream result. |
| `relay_upstream_duration_seconds` | histogram | `provider` | Upstream call latency. |
| `relay_auth_failures_total` | counter | `reason` (`bad_format`,`unknown_prefix`,`hash_mismatch`,`revoked`,`expired`,`account_disabled`) | Auth rejections. **`/metrics` is internal/scrape-only** — the `reason` split (which distinguishes `unknown_prefix` from `hash_mismatch`) must **never** be exposed to a tenant or untrusted scraper, or it becomes a key-enumeration oracle paired with the constant-time auth path (§8.2). |
| `relay_rate_limited_total` | counter | `account_id`(low-card, hashed/bucketed if needed), `kind` (`account`,`token`) | 429s emitted. |
| `relay_rate_limit_degraded_total` | counter | — | Fail-open events when Redis is down (§9.4). |
| `relay_idempotency_total` | counter | `result` (`first`,`replay`,`conflict`,`reuse`) | Idempotency decisions. |
| `relay_apns_jwt_refresh_total` | counter | `result` (`scheduled`,`expired_retry`,`throttled`) | JWT lifecycle (§6.1). |
| `relay_fcm_token_refresh_total` | counter | `result` | OAuth2 token refreshes (§7.1). |
| `relay_redis_up` / `relay_pg_up` | gauge | — | Core dependency health (drives `/readyz` — §5.4). |
| `relay_provider_healthy` | gauge | `provider` | Per-provider init/circuit-breaker health (`1` healthy / `0` degraded) — does **not** gate `/readyz` (§5.4). |
| `relay_provider_credential_healthy` | gauge | `provider` | Synthetic credential-validation result (§13.4); `0` triggers the credential alert. |
| `relay_fcm_project_quota_headroom` | gauge | `project_id` | Remaining headroom against the shared FCM project quota (§9.5). |
| `relay_daily_quota_used_ratio` | gauge | `account_id`(low-card/bucketed) | Fraction of the 50k/day cap consumed; alerts ≥80% (§9.2). |
| `relay_connection_severed_total` | counter | `provider` | Sends mapped to `503` by GOAWAY/connection reset before a final response (§6.4). |
| `relay_authfail_limiter_degraded_total` | counter | — | Per-IP auth-failure limiter fail-open events (§8.2). |

### 13.3 Admin diagnostics

`relayctl` provides read paths for operators (no raw secrets/tokens): `account list`, `key list`, `allowlist show`, and a `relayctl logs --account <id> [--since …] [--outcome rejected]` that queries `relay_op_logs` (redacted by construction). A `relayctl ping-upstream --provider apns|fcm --env sandbox` performs a credential-only health check (JWT sign / token mint, optionally an FCM `validateOnly:true` send) without sending real content — useful after a credential rotation. The same check captures the sandbox `apns-unique-id` (§5.1) when APNs returns one.

### 13.4 Credential health, validation and rotation alerting

Credentials can be revoked or rotated **out from under the relay** at any time — Apple "actively monitors and revokes leaked `.p8` keys" (`00`), and FCM SA keys / Workload-Identity bindings can be disabled. Relying only on production sends starting to fail (upstream `403 InvalidProviderToken` / the relay's own `401 UNAUTHENTICATED` / `403 PERMISSION_DENIED`) is too late. The relay therefore validates credentials **proactively**, distinguishing an **auth-layer** failure from a mere token-layer rejection:

- **Startup + periodic synthetic validation.** On startup and on a periodic timer, the relay exercises the auth layer end-to-end: an FCM `validateOnly:true` send, and an APNs send to a **known-bad token** that still forces APNs to authenticate the JWT (so an auth-layer `403 InvalidProviderToken`/`BadCertificate*` is distinguishable from a token-layer `400 BadDeviceToken` — the latter proves the credential is *accepted*). Note `/readyz` alone is **insufficient**: it only proves a JWT was **signed locally** / a token **minted**, not that APNs/FCM **accept** the credential.
- **`credential healthy` gauge.** Wire the synthetic-validation result into a per-provider `relay_provider_credential_healthy` gauge (§13.2) and surface it on the per-provider readiness signal (§5.4).
- **Continuous health via cron.** Schedule `relayctl ping-upstream` (e.g. a cron / scheduled run) for continuous credential health between restarts.
- **Alert on first auth failure.** Alert on the **first** `upstream_auth_error` (the relay's own credential signals — §6.4/§7.4), not after a sustained failure rate, since a revoked credential fails *all* sends immediately.

---

## 14. Deployment and Operations

### 14.1 Public endpoint & TLS

- One public hostname, `relay.silo.app`, terminating TLS (1.2+, prefer 1.3) at a load balancer or in-process. HTTP/2 end to end. Only `/v1/apple/send`, `/v1/fcm/send`, and `/healthz` are public; `/readyz` and `/metrics` are internal-only.
- Set explicit server timeouts and graceful shutdown (§11).

### 14.2 High availability — "relay-down delays, never loses"

The relay being down does **not** lose pushes: the self-hosted server's dispatch is an **outbox**. The fanout transaction in `../01-release-events-and-inbox.md` inserts `pending` `push_delivery_attempts` rows in the same transaction as the `notification_deliveries` row; the dispatcher claims them post-commit and a recovery sweeper re-claims stale `pending` rows (`00`/`02`/`03`). So if the relay returns `503`/times out, the caller marks the attempt `retrying` and retries with backoff — the durable inbox row and the outbox attempt row guarantee eventual delivery. **This means the relay can be operated with ordinary HA (multiple stateless replicas, rolling deploys) rather than five-nines durability.** A short relay outage only delays pushes.

- **Stateless replicas.** No in-memory request state that can't be reconstructed; idempotency and rate-limit state are in Redis, shared across replicas. Scale horizontally behind the LB.
- **Rolling deploys / kill-safety.** A replica can be drained (`srv.Shutdown`) or killed mid-request; the caller retries the same idempotency key, which either replays the recorded result or re-sends at-most-once (§10).
- **Drain ordering (no orphaned idempotency locks).** A clean drain must not turn into a 30 s stall per retry. On `SIGTERM`: (1) flip `/readyz` to `503` so the LB **stops routing new requests** to this replica; (2) keep serving **in-flight** handlers; (3) bound the `srv.Shutdown` grace period to **less than the idempotency `lock_ttl`** (§10.2) — and/or **release/downgrade the in-flight idempotency marker on handler context-cancellation** — so that a send killed mid-flight does **not** leave a stale 30 s `in_flight` marker in Redis. Otherwise the caller's immediate retry hits §10.2 case 4 and gets `409 idempotency_conflict` for up to `lock_ttl` seconds. The relationship between the shutdown grace period and `lock_ttl` is load-bearing and is asserted by a drain test (no orphaned in-flight markers after a rolling restart — see `01-implementation-plan.md`).

### 14.3 Secret provisioning

- Provision the APNs `.p8` (+ Team ID, Key ID) and FCM SA JSON (or Workload Identity binding) into the secret manager out-of-band. The HMAC pepper and DB/Redis creds likewise. Never in the image or repo (§11).
- **Credential rotation runbook (overlap window, dual-key).** Apple permits **multiple active `.p8` keys per team**, and the relay already keys connection pools by team (§6.1), so rotation is **make-before-break**: (1) provision the new credential into the secret manager alongside the old; (2) load both — the relay supports an **overlap window** where old and new keys are both valid and the key-selection map (§6.1) can resolve to either; (3) run `relayctl ping-upstream` / the synthetic validation (§13.4) to confirm the **new** credential authenticates against APNs/FCM; (4) cut sends over to the new key (rolling restart picks up the new secret); (5) retire/revoke the old credential only after the new one is confirmed healthy. The same overlap pattern applies to FCM SA keys / Workload-Identity bindings. This avoids the failure mode where a single-key rotation revokes the old credential before the new one is proven, taking all pushes down.

### 14.4 Sandbox vs production

- The relay supports both APNs environments via the request `environment` field, routing to `api.push.apple.com` (production) or `api.sandbox.push.apple.com` (sandbox) (§6.2). FCM has a single global host (no sandbox split — reference §2.3); test sends use `validateOnly:true` (reference §2.5).
- A **staging relay deployment** (separate hostname, separate accounts) is recommended for client QA so test traffic never touches production accounts/quotas.

### 14.5 Scaling & regions

- **Scaling.** One well-tuned APNs HTTP/2 connection handles thousands of pushes/sec (reference §1.3/§3.1); one FCM token source serves the project. The bottleneck at relay scale is inbound throughput and Redis, both horizontally scalable. Keep upstream clients **process-singletons** (one per env/team/project), not per-request.
- **Regions.** APNs and FCM are global; FCM has no data-residency send endpoints (reference §2.3). v1 runs a single region close to the operator's Postgres/Redis. Multi-region is possible later (the request path is stateless; Redis/Postgres would need regional strategy) but is out of scope for v1.
- **Quota awareness.** FCM's default project quota is ~600k messages/min (reference §2.7); the relay's per-account 50k/day default (§9.2) is far under that even summed across many accounts. The **shared** project quota is nonetheless guarded by the relay-side global limiter / circuit breaker (§9.5) so one noisy account cannot exhaust it for all tenants; operators monitor `relay_fcm_project_quota_headroom` (§13.2), the project quota in Cloud Console, and `relay_upstream_requests_total{upstream_reason="QUOTA_EXCEEDED"}`.

---

## 15. Open Questions

1. **Per-account rate limits — real numbers.** v1 ships the `00`-suggested ~10 req/s burst + 50k/day defaults (§9.2); tune from production logs. Should limits become per-account columns on `relay_accounts` with `relayctl quota set`? (Ties to `00` open question #1.)
2. **APNs sandbox hostname (resolved — note only).** `api.sandbox.push.apple.com` and `api.development.push.apple.com` are **interchangeable, both-resolving** Apple sandbox aliases; the relay standardizes on `api.sandbox.push.apple.com` (reference §1.3, reference open question §5 item 4). The external contract (`"environment":"sandbox"`) is unaffected either way. No build-time decision required.
3. **`apns2` vs hand-rolled APNs client.** `sideshow/apns2` has had no release since v0.25.0 (Oct 2024) — bus-factor risk. Decide before implementation: adopt `apns2` or hand-roll `net/http2` + `golang-jwt/jwt/v5` (reference §3.1, §5.2).
4. **FCM credentials: GCP-keyless vs off-GCP JSON.** If the relay runs on GCP, use Workload Identity / attached SA (no downloadable key); off-GCP, the SA JSON lives in the secret manager. Resolve in the deployment design (reference §4.5 / §2.2).
5. **`apns-expiration` / `android.ttl` default values (resolved; tune from product input).** v1 **ships finite defaults** — `apns-expiration` ~4 h for `private_alert` / ~1 h for `background_wake`, and the parallel FCM `android.ttl` (§5.1/§5.2) — rather than the 30-day APNs / 4-week FCM store-and-retry, so stale wakes don't fire days later. The exact durations are configurable and may be tuned with product input (reference §1.4).
6. **API-key hashing: HMAC pepper vs plain salted SHA-256.** This spec chooses HMAC-SHA-256 with a secret-manager pepper for defense-in-depth. Pepper rotation is operationally heavier (re-hash all keys or support dual peppers). Confirm the tradeoff (reference §4.1, open question §12).
7. **Idempotency-Key spec status.** The IETF draft expired 2026-04-18 without becoming an RFC; the header name + Stripe-style semantics are de-facto standard. Re-check for an RFC before relying on its exact status-code semantics (reference §4.3).
8. **Op-log volume & retention.** At 50k/day/account across many accounts, `relay_op_logs` grows unbounded without GC. v1 **ships** the retention sweep (delete success >14 d, failure >90 d — §13.1) as a scheduled `relayctl logs --gc` / built-in sweeper (`01-implementation-plan.md` Phase 5), with a table-growth alert. Open: whether to **time-partition** `relay_op_logs` (recommended) or offload to a log pipeline rather than Postgres at higher volume.
9. **Multi-region.** Out of scope for v1 (single region). If latency to distant self-hosters matters, design a regional Redis/Postgres strategy for the otherwise-stateless request path.
10. **FCM priority deprioritization.** The relay honors `03`'s `priority: high` for `private_data`. If FCM deprioritizes Silo's app-instance traffic over the 7-day window (reference §2.8), is any relay-side change warranted, or is this purely the caller's `priority: normal` fallback concern? Lean: caller's concern; revisit if metrics show systematic deprioritization.
