# Silo Push Relay — Design Index

> **Provenance:** Authored 2026-06-13 from a research-and-review workflow (live primary-source research against Apple Developer / Firebase / Google / RFC / pkg.go.dev docs, adversarial fact-checking of time-sensitive claims, draft, multi-lens review, and revision). Reflects **2026-current** APNs and FCM behavior. These design docs live here in `silo-server` alongside the `02`/`03` notification contracts purely for **co-location** — the relay **code** lives in its own repository (provisional name `silo-push-relay`).

This folder specifies the **Silo Push Relay**: the small Silo-operated service that holds the official Apple (`.p8` APNs auth key) and Google (Firebase service-account JSON) push credentials and forwards **opaque, content-free** push requests to APNs and FCM on behalf of opted-in self-hosted Silo servers.

It is the **server counterpart** to the relay-client contracts in [`../02-apns-relay.md`](../02-apns-relay.md) and [`../03-fcm-relay.md`](../03-fcm-relay.md). Those documents define the wire contract from the self-hosted Silo server's perspective (`POST /v1/apple/send`, `POST /v1/fcm/send`); this folder specifies the service that answers it. **That external contract is authoritative and reproduced byte-for-byte; these docs add only the server-internal design (`02`/`03` deliberately left it "to a separate repo").**

## Reading order

1. **[`00-relay-spec.md`](./00-relay-spec.md)** — Engineering spec. Scope, trust/threat model, architecture, the full public API contract (both send endpoints + health/metrics + the `relayctl` admin CLI), the upstream APNs and FCM clients, the PostgreSQL account/key/allowlist schema, rate limiting, idempotency, security, privacy, observability, deployment.
2. **[`01-implementation-plan.md`](./01-implementation-plan.md)** — Phased Go build plan. Repo bootstrap and layout, pinned dependency choices with rationale, seven implementation phases (each with files, tasks, tests, acceptance criteria), testing strategy, deployment, and the Apple/Firebase provisioning checklist.
3. **[`02-apns-fcm-2026-reference.md`](./02-apns-fcm-2026-reference.md)** — The cited 2026 factual foundation. APNs (token/ES256 auth, endpoints, headers, payloads, status/reason tables, throttling) and FCM v1 (legacy-shutdown status, OAuth2 auth, endpoint, message JSON, error model, quotas) plus the recommended Go ecosystem and security/ops practices. Every non-obvious claim carries an inline source + date.
4. **[`03-decisions.md`](./03-decisions.md)** — Decision record resolving the spec's open implementation questions (APNs client, FCM credentials, `apns-expiration`, rate limits, API-key hashing, op-log retention), each with a firm recommendation, alternatives, rationale, consequences, and cited current sources.

## Status

| Doc | Status |
|---|---|
| 00-relay-spec | Draft (implementation-grade) |
| 01-implementation-plan | Draft (implementation-grade, phased) |
| 02-apns-fcm-2026-reference | Reference (2026-current, cited) |
| 03-decisions | Decided (6 resolutions, high confidence) |

**Implementation status:** Phases 0–2 are built in the separate `silo-push-relay` repository. Phase 0 = the service skeleton (`/healthz` + `/readyz`, config, JSON logging, §5.5 envelope, graceful shutdown). Phase 1 = storage + admin (PostgreSQL schema via embedded Goose migrations, the `accounts` data-access layer with `HMAC-SHA256(pepper)` key hashing, the `relayctl` admin CLI with audited actions). Phase 2 = the full request pipeline on `POST /v1/apple/send` + `/v1/fcm/send` — bearer auth (constant-time compare with decoy, throttled `last_used_at`, soft per-IP brute-force cap, trusted-proxy client-IP resolution), Redis token-bucket rate limiting (per-account + daily + coarse per-token) with bounded in-process fail-open, `DisallowUnknownFields` decode/validate/allowlist, and fail-closed idempotency (NX lock + nonce, replay/409/422, compare-and-set). Upstream APNs/FCM calls are **stubbed** pending Phases 3–4. Two Phase-2-listed items were deferred to Phase 4 because they need the live FCM send path: the shared FCM-project circuit breaker (§9.5) and anti-spike smoothing. Phases 3–6 not started; provider-touching phases gated on Apple/Firebase provisioning (see `01` §7). `relay_op_logs` partitioning (Decision 6) is a dedicated later migration so migrations run on stock PostgreSQL.

## Key design decisions

- **Faithful external contract.** `POST /v1/apple/send` and `POST /v1/fcm/send` — request fields, validation, response shapes, relay-built payloads, and APNs headers — are reproduced exactly from `02`/`03`. Only the relay's server-internal design is newly specified.
- **Content-free by construction.** The send structs admit only the opaque `02`/`03` fields; JSON is decoded with `DisallowUnknownFields`, so any unexpected key is a hard `400`. FCM is always data-only (no `notification` block); badge omitted by default.
- **Stateless request path.** The only synchronous DB touch is an indexed API-key prefix lookup plus a throttled (`≤1/min`) `last_used_at` write; idempotency and rate-limit state live entirely in Redis, so replicas scale horizontally and are kill-safe.
- **Admin = CLI, not HTTP.** Account/key/allowlist administration is `relayctl` writing directly to PostgreSQL; there is **no public admin HTTP API** in v1 (minimal attack surface).
- **API keys.** `rk_<env>_<random>` with a non-secret indexed prefix + `HMAC-SHA256(pepper, secret)` stored, constant-time compare, revocable, multi-live-key rotation; the full token is shown exactly once.
- **Upstream mechanics (2026-correct).** One cached ES256 JWT per APNs team (regenerate ~50 min, never < 20 min apart, eager refresh on `ExpiredProviderToken`); one cached OAuth2 token per FCM project (~1 h); HTTP/2 connection reuse with PING health checks.
- **Idempotency fails closed; rate limiting fails open.** If Redis is unavailable, sends are refused (`503`) rather than risk a double-send; rate limiting degrades to allow (availability over perfect enforcement), with a metric.
- **"Relay-down delays, never loses."** The self-hosted server-side outbox (`push_delivery_attempts` pending rows in `../01-release-events-and-inbox.md`) guarantees re-delivery, so ordinary stateless-replica HA suffices.

### Reconciliations where the 2026 reference corrects the 2026-04 contracts

These are upstream-only details; the **external `02`/`03` contract is unchanged**:

- **APNs sandbox host** is `api.sandbox.push.apple.com` (the canonical name); `02`'s `api.development.push.apple.com` is a still-valid alias (same backend). Internal detail; the external `"environment":"sandbox"` field is untouched.
- **FCM 429** honored with `Retry-After` (60 s fallback) while still surfacing `Retry-After` to the caller as `03` expects.
- **FCM data values** forced to strings (`map<string,string>`).
- **FCM `priority:high`** honored for `private_data`; provider deprioritization is the caller's concern.

## Decisions (resolved in `03-decisions.md`)

The spec's open implementation questions are now decided (all high confidence):

1. **APNs client** — **hand-roll** on `net/http` + `x/net/http2` + `golang-jwt/jwt/v5` behind an `internal/apns` interface; do **not** take a production dependency on `sideshow/apns2` (no release since Oct 2024). Optionally keep apns2 as a throwaway Phase-3 oracle.
2. **FCM credentials** — **keyless by default** via Application Default Credentials: attached service account on GCP, Workload Identity Federation off-GCP; service-account JSON in a secret manager only as a last resort. Identical code path in all three cases.
3. **`apns-expiration`** — relay sets a **finite TTL** (4 h for `private_alert`, 1 h for `background_wake`) with a parallel FCM `android.ttl`; relay-owned config, external `02`/`03` contract unchanged.
4. **Per-account rate limits** — **hard-coded global defaults** for v1 (~10 req/s burst, 50k/day), resolved through a typed `accountLimits` struct so per-account override columns are a cheap additive migration later.
5. **API-key hashing** — `HMAC-SHA256(pepper, secret)` with a `pepper_version` column and a make-before-break dual-pepper rotation window. No argon2id (keys are high-entropy).
6. **Op-log retention** — native PostgreSQL **daily RANGE partitioning**, retention by **partition DROP** (pg_partman), uniform 90-day horizon with a cheap row-level success-trim inside young partitions.

## Confirmed (2026-06-13)

- **Module org** — `github.com/Silo-Server/silo-push-relay` (matches the real org in `silo-server`'s `go.mod`; the plan's `silo-app` was provisional).
- **Deployment target** — the relay is **not** deployed to GCP. Per Decision 2 this selects the off-GCP credential path: **Workload Identity Federation** (keyless, non-secret credential-config + OIDC token) preferred, with **service-account JSON in a secret manager** as the last-resort fallback. No attached-GCP-SA path.

## Still needs human / operational input

1. **Provider provisioning** (the real long pole) — Apple Developer account + APNs `.p8` key + per-platform bundle topics; Firebase project(s) + the WIF binding (or SA JSON in a secret manager). Gates Phases 3/4/6. See `01` §7.
2. **Off-GCP OIDC issuer for WIF** — confirm the relay host has a usable OIDC issuer for Workload Identity Federation; if none, fall back to SA-JSON-in-secret-manager (Decision 2).
3. **Multi-region** — out of scope for v1 (single region); revisit a regional Redis/Postgres strategy if latency to distant self-hosters matters.

## Relationship to the parent notifications design

This relay corresponds to phases 7–8 of the parent plan ([`../README.md`](../README.md) "Phasing"): `notifications.apple_push_enabled` and `notifications.android_push_enabled`. Those phases require Silo to provision Apple Developer + Firebase accounts and operate this service. The self-hosted server side (registration, dispatcher, pacing, retry, the dispatch outbox) is specified in `../02-apns-relay.md`, `../03-fcm-relay.md`, and `../01-release-events-and-inbox.md`.
