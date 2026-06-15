# Silo Push Relay — Implementation Plan

**Date:** 2026-06-13
**Status:** Draft (implementation-grade, phased)
**Service:** `silo-push-relay` (provisional repo name; the relay code lives in a **separate repository**)
**Provisional hostname:** `relay.silo.app`
**Stack:** Go 1.25+, PostgreSQL, Redis

**Source documents (read before executing any phase):**

- [`./00-relay-spec.md`](./00-relay-spec.md) — the engineering spec this plan implements. Section numbers below (e.g. "spec §6.4") refer to it.
- [`./02-apns-fcm-2026-reference.md`](./02-apns-fcm-2026-reference.md) — 2026-current Apple/Google/Go reference (cited as "reference §N").
- [`../02-apns-relay.md`](../02-apns-relay.md) and [`../03-fcm-relay.md`](../03-fcm-relay.md) — the **authoritative external wire contract** (the `02`/`03` request/response shapes). The relay must honor these byte-for-byte.

> **Commands assume the repository root is the cwd.** This document contains no local absolute filesystem paths or transient worktree IDs; all repo references are repository-relative.

> **Repo boundary.** This plan describes a **separate repository** (`silo-push-relay`). The design docs are authored here in `silo-server` purely for co-location with the `02`/`03` contracts. The relay **code** lives elsewhere. The one deliberate coupling is the shared `pushwire` payload-builder package, mirrored into both repos (see §3 and Phase 3/4).

---

## 1. Overview

### 1.1 What is being built

A small, **stateless-on-the-request-path** Go HTTP service that holds the official Silo Apple (`.p8` APNs auth key) and Google (Firebase service-account JSON) push credentials, accepts authenticated **content-free** push requests from opted-in self-hosted Silo servers on two endpoints, builds a fixed generic APNs/FCM payload, forwards it upstream, and maps the upstream result back to a narrow caller-facing response.

The two send endpoints and their contract are fixed by `02`/`03` and reproduced in spec §5:

| Endpoint | Purpose | Authoritative contract |
|---|---|---|
| `POST /v1/apple/send` | Forward one opaque push to APNs for one device token | `../02-apns-relay.md`, spec §5.1 |
| `POST /v1/fcm/send` | Forward one opaque data-only push to FCM for one device token | `../03-fcm-relay.md`, spec §5.2 |
| `GET /healthz` | Liveness (public) | spec §5.3 |
| `GET /readyz` | Readiness incl. PG/Redis core deps + per-provider credential health (internal) | spec §5.4 |
| `GET /metrics` | Prometheus exposition (internal) | endpoint spec §5.4; metric catalog §13.2 |

Administration is a CLI (`relayctl`) that writes directly to the relay's PostgreSQL — **there is no public admin HTTP API** in v1 (spec §5.6).

### 1.2 The v1 cut line

**In scope (v1):**

- `POST /v1/apple/send` and `POST /v1/fcm/send` exactly per `02`/`03`.
- Bearer auth (`rk_` keys), per-account allowlists, Redis token-bucket rate limiting, Redis idempotency.
- APNs token-based (ES256 `.p8`) client with cached JWT; FCM HTTP v1 client with cached OAuth2 token.
- PostgreSQL storage of accounts/keys/allowlists/redacted op-logs only; Goose-style migrations.
- `relayctl` admin CLI; structured redacted logs; Prometheus metrics; `/healthz` + `/readyz`; graceful shutdown.
- Single region, multiple stateless replicas behind a TLS load balancer.

**Explicitly out of scope (v1)** — from spec §2.2 / NG1–NG7:

- Token→user aliases, device subscriptions, any user/profile identity (stateless v1).
- Arbitrary APNs/FCM JSON, custom titles/bodies, topic/condition broadcasts, badge-by-default.
- Delivery receipts, analytics, open-tracking.
- APNs broadcast / Live Activity channels (`/4/broadcasts/...`).
- Web Push, HMS, ADM, or any non-APNs/non-FCM transport.
- A public self-service signup / billing surface.
- The `custom_apns` / `custom_fcm` direct paths (those run inside `silo-server`).
- Multi-region (the request path is stateless, but Redis/Postgres regional strategy is deferred).

### 1.3 The real long pole: provider account provisioning

**Apple/Firebase account provisioning is the critical-path dependency, not the code.** Obtaining the official Apple Developer team, generating a `.p8` APNs auth key (Key ID + Team ID), registering per-platform bundle topics, standing up the Firebase project(s), and minting a service-account JSON can take days-to-weeks of organizational/approval lead time and is **fully decoupled from coding**.

> **Action: start the §7 provisioning checklist on day 1, in parallel with Phase 0.** Phases 0–2 (scaffold, storage, auth/rate-limit/idempotency) need **no** live provider credentials and can complete entirely against fakes and contract tests. Only Phase 3 (APNs integration tests against sandbox) and Phase 4 (FCM `validateOnly`) are gated on credentials. If provisioning slips, the code still reaches "everything but live upstream" without blocking.

```mermaid
flowchart LR
  subgraph Track A — Code
    P0[Phase 0\nscaffold] --> P1[Phase 1\nstorage] --> P2[Phase 2\nauth/RL/idem]
    P2 --> P3[Phase 3\nAPNs]
    P2 --> P4[Phase 4\nFCM]
    P3 --> P5[Phase 5\nobservability]
    P4 --> P5
    P5 --> P6[Phase 6\nharden+deploy]
  end
  subgraph Track B — Provisioning (parallel, long pole)
    A1[Apple team + .p8 + topics]
    G1[Firebase project + SA JSON + package]
  end
  A1 -. gates .-> P3
  G1 -. gates .-> P4
  A1 -. gates .-> P6
  G1 -. gates .-> P6
```

---

## 2. Repository Bootstrap

### 2.1 Go module path

```
module github.com/silo-app/silo-push-relay
```

Target **Go 1.25+** (required by `jackc/pgx v5.10` and `firebase.google.com/go/v4 v4.20`; reference §3 version-targeting note). Pin in `go.mod` with `go 1.25`.

### 2.2 Directory layout

```
silo-push-relay/
├── cmd/
│   ├── relay/                  # main HTTP service entrypoint (wires config → stores → clients → server)
│   │   └── main.go
│   └── relayctl/               # admin CLI (account/key/allowlist/logs/ping-upstream); writes directly to PG
│       └── main.go
├── internal/
│   ├── config/                 # env + secret-manager config loading, validation, defaults
│   ├── httpapi/                # router, handlers, middleware (auth, rate limit, idempotency, redaction), error bodies
│   ├── apns/                   # APNs upstream client: ES256 JWT lifecycle, HTTP/2 pool, reason→error mapping
│   ├── fcm/                    # FCM upstream client: OAuth2 token source, HTTP/2 client, status→error mapping
│   ├── accounts/              # data-access for accounts/keys/allowlists (shared by httpapi + relayctl)
│   ├── ratelimit/              # Redis Lua token-bucket limiter (per-account + coarse per-token)
│   ├── idempotency/            # Redis SET NX lock / replay / 409 / 422 store
│   ├── pushwire/               # SHARED payload builders (APNs + FCM payload/header construction) — mirrored into silo-server
│   ├── oplog/                  # redacted op-log writer (PG) + token hashing helper
│   ├── observability/          # slog JSON handler + redaction, Prometheus registry/metrics, /healthz /readyz /metrics
│   └── store/                  # pgxpool + go-redis bootstrapping, health pings, migration runner hook
├── migrations/
│   └── sql/                    # Goose-style timestamped SQL migrations (see §2.6)
├── deploy/
│   ├── Dockerfile
│   └── (k8s / compose manifests as applicable)
├── Makefile
├── go.mod
├── go.sum
├── .golangci.yml
├── .github/workflows/ci.yml    # or equivalent CI config
└── README.md
```

Package ownership mirrors the Silo convention (CLAUDE.md "keep new code in the package that owns the behavior"): no catch-all `utils` package; shared payload logic lives in `pushwire`; shared data-access lives in `accounts`/`store` so `relayctl` and `httpapi` share schema and constraints (spec §5.6 "The CLI shares the relay's data-access package").

### 2.3 The shared `pushwire` package (maintainability win)

`internal/pushwire` is the single source of truth for **how an opaque request becomes an upstream payload + headers** — the APNs `aps`/`silo` dictionaries and `apns-*` headers (spec §5.1), and the FCM data-only `message` body + `android` config (spec §5.2). It is intentionally **dependency-light** (pure Go structs + JSON, no network) so it can be **mirrored into `silo-server`** under its `custom_apns` / `custom_fcm` paths, where the self-hosted server builds the *same* payloads when an admin uses their own credentials. One package, two repos: the relay and the direct-credential path can never drift on payload shape. (See §3 dependency table and Phase 3/4.)

> **Mirroring mechanism & sync runbook (drift/ownership hazard — make it explicit).** Keep `pushwire` import-cycle-free and free of relay-only types (no DB, no config). **Designate one repo as the canonical source of truth** for `pushwire` (the relay repo `silo-push-relay`, since it owns the upstream payload contract). The copy in `silo-server` is a **checked-in generated artifact**, not a hand-edited file: commit a **content checksum** of the canonical package alongside it, and have **both** repos' CI verify their local copy against that checksum (a copied package in repo A cannot fail repo B's CI on its own, so a passive golden-file test in one repo is insufficient). Bump a **versioned payload-schema constant** on any payload change so a mismatch is unambiguous. Add a golden-file + closed-allowlist-key contract test on both sides (Phase 3/4 / §5.2). Document the ownership/sync runbook (who regenerates, in what order, how the checksum is bumped) so a payload change cannot silently diverge until the other repo happens to update. The preferred end state is a small shared module both repos import rather than a manual copy.

### 2.4 Config strategy

Twelve-factor-ish, but credentials come from a **secret manager**, not env vars (spec §11, reference §4.5 ranking: secret-manager > mounted file > env var > in-image). `internal/config` resolves, in order of preference per secret:

| Config | Source (preferred → fallback) | Notes |
|---|---|---|
| APNs `.p8` private key, Key ID, Team ID | secret manager → mounted file | never env var, never in image |
| FCM SA JSON **or** Workload Identity binding | GCP attached SA / Workload Identity → secret manager JSON | off-GCP uses JSON; on-GCP keyless (reference §4.5) |
| API-key HMAC pepper | secret manager | rotation is heavier (spec open Q#6) |
| Postgres DSN, Redis URL | secret manager → env | pool-tuned |
| Non-secret tuning (timeouts, rate defaults, listen addrs, log level, environment label) | env vars / flags | safe to log |

`config.Load(ctx)` returns a validated, fully-populated struct or a hard error; the process refuses to start with a missing/invalid credential. Provide a `config.Validate()` that `cmd/relay` calls before opening any listener. Non-secret defaults (rate limits from spec §9.2, timeouts from spec §11) live as constants with env overrides.

### 2.5 Makefile (mirror Silo's Makefile-driven workflow)

`make` targets, modeled on `silo-server`'s Makefile (CLAUDE.md §Build):

```make
make build            # go build ./cmd/relay ./cmd/relayctl
make run              # run cmd/relay locally against docker-compose PG+Redis
make test             # go test ./... (unit + contract; no live providers)
make test-integration # go test -tags=integration ./...  (APNs sandbox + FCM validateOnly; needs creds)
make lint             # golangci-lint run
make fmt              # gofmt -w + goimports
make migrate-create NAME=add_thing   # timestamped Goose migration scaffold
make migrate-up       # apply migrations
make migrate-status   # list migration state
make relayctl ARGS="account list"    # build+run the admin CLI
make docker           # build deploy/Dockerfile
make loadtest         # run the k6/vegeta burst scenario (Phase 6)
```

`make migrate-create NAME=...` must produce a **timestamped** filename (CLAUDE.md: "New migrations must use timestamped filenames created with `make migrate-create`; do not run `goose fix` or create paired `.up.sql`/`.down.sql` files"). The relay uses single-file Goose SQL migrations with `-- +goose Up` / `-- +goose Down`.

### 2.6 Migrations (Goose-style)

`migrations/sql/<timestamp>_<name>.sql`, run by a Goose runner wired in `internal/store` and invokable via `make migrate-up` and on `cmd/relay` startup (behind a flag). The v1 schema is exactly spec §8.1 (`relay_accounts`, `relay_api_keys`, `relay_apns_allowlist`, `relay_fcm_allowlist`, `relay_op_logs`). IDs are ULIDs stored as `text`; timestamps are `timestamptz` (Silo convention).

### 2.7 CI outline

CI (GitHub Actions or equivalent) runs on every push/PR:

```yaml
jobs:
  build-test:
    steps:
      - setup-go 1.25
      - make lint                # golangci-lint
      - make build
      - make test                # unit + contract tests (no provider creds)
      - upload coverage
  migrate-check:
    services: [postgres, redis]
    steps:
      - make migrate-up          # migrations apply cleanly on a fresh DB
      - make migrate-status
  integration:                   # gated: only when provider creds are available (Phase 3+)
    if: secrets.APNS_P8 != '' && secrets.FCM_SA_JSON != ''
    services: [postgres, redis]
    steps:
      - make test-integration    # APNs sandbox + FCM validateOnly contract tests
```

The `integration` job is **conditional on secrets being present** so Phases 0–2 are green long before any credential exists.

---

## 3. Dependency Choices

All versions are from the reference §3.7 pinned list. Pin exact versions in `go.mod`; bump deliberately.

| Concern | Library | Version | One-line rationale |
|---|---|---|---|
| APNs client | `github.com/sideshow/apns2` **or** hand-rolled | `v0.25.0` / `x/net/http2 v0.56.0` + `golang-jwt/jwt/v5 v5.3.1` | De-facto Go APNs HTTP/2 client (auto JWT + conn reuse); **decide vs. hand-rolled before Phase 3** because `apns2` has had no release since Oct 2024 (bus-factor; reference §3.1, spec open Q#3). |
| FCM auth | `golang.org/x/oauth2/google` | `v0.36.0` | `CredentialsFromJSONWithType(..., google.ServiceAccount, firebase.messaging scope)` gives an auto-caching/-refreshing `TokenSource`; lighter than the Admin SDK and gives direct control of conn reuse + error classification (reference §3.2 — preferred for a fixed payload). |
| FCM (alt, batteries-included) | `firebase.google.com/go/v4/messaging` | `v4.20.0` | Acceptable alternative with `IsUnregistered`/`IsQuotaExceeded` helpers; **pick exactly one** FCM path, not both (reference §3.2). Plan defaults to the `x/oauth2` path. |
| JWT (ES256 sign for APNs; RS256 for hand-rolled FCM grant) | `github.com/golang-jwt/jwt/v5` | `v5.3.1` | Maintained JWT lib; ES256 for APNs, RS256 if hand-rolling the FCM token exchange (reference §3.1). |
| HTTP/2 transport tuning | `golang.org/x/net/http2` | `v0.56.0` | `ReadIdleTimeout`+`PingTimeout` to reap half-open upstream conns — "the single most important tuning for a long-lived relay" (reference §3.3). |
| Postgres | `github.com/jackc/pgx/v5` + `pgxpool` | `v5.10.0` | Recommended driver/pool; `pgxpool.New` is concurrency-safe + health-checked (reference §3.4). |
| Redis | `github.com/redis/go-redis/v9` | `v9.20.1` | Context-first client with pooling; atomic Lua for rate-limit, `SET NX` for idempotency (reference §3.4). |
| Router | stdlib `net/http` (Go 1.22+ `http.ServeMux` patterns) | stdlib | Two routes + health/metrics need no framework; stdlib `ServeMux` supports method+path patterns and gets HTTP/2 over TLS automatically. Avoids a dependency; middleware is plain `http.Handler` wrapping. |
| Structured logging | `log/slog` | stdlib (Go 1.21+) | `slog.NewJSONHandler` for production; redaction middleware so secrets can't leak (reference §3.5, spec §13.1). |
| Metrics | `github.com/prometheus/client_golang` | `v1.23.2` | Custom registry + `promhttp.HandlerFor` for `/metrics`; per-provider latency histograms + outcome counters (reference §3.5, spec §13.2). |
| Per-instance rate (outbound pacing helper) | `golang.org/x/time/rate` | current | `Reservation.Delay()` to compute relative `Retry-After`; used for in-process pacing, **not** the cross-replica cap (that is Redis — reference §4.2). |
| ULID generation | `github.com/oklog/ulid/v2` (or equivalent) | current | Generate `request_id` and entity IDs as ULIDs (Silo convention; spec §8.1). |

**Decision to make before Phase 3 (spec open Q#3):** adopt `sideshow/apns2` or hand-roll the APNs client. Recommendation: prototype both in Phase 3 behind the `internal/apns` interface; pick `apns2` if its recent commit/issue activity is acceptable, else hand-roll on `x/net/http2` + `golang-jwt/jwt/v5` mirroring the verified `apns2` internals (reference §3.1: `HostProduction`/`HostDevelopment`, `TokenTimeout=3000`s, `ReadIdleTimeout=15s`). Either way the rest of the service depends only on the `internal/apns` interface, not the concrete client.

**`pushwire` maintainability note (reference + spec §5).** The shared `pushwire` package builds the exact APNs/FCM payloads and headers. Because `silo-server`'s `custom_apns`/`custom_fcm` paths must produce identical payloads, `pushwire` is mirrored into `silo-server` with **one canonical source repo** (the relay), a **committed checksum verified in CI on both sides**, and a **closed-allowlist-key** test so an added `data`/payload key fails CI regardless of golden-file regeneration order (§2.3 sync runbook, §5.2). This is also a **supply-chain control for the content-free guarantee**: the payload-builder that enforces "data-only, no `notification` block, fixed key set" is enforced in two repos that must not drift. Payload shape is defined once.

---

## 4. Phased Tasks

Effort sizes: **S** ≈ 1 day, **M** ≈ 2–3 days, **L** ≈ 4–5 days (one engineer). Sizes are rough.

### Phase 0 — Scaffold, config, health, CI  (size: M)

**Goal.** A buildable, lintable, testable repo that boots `cmd/relay`, serves `/healthz`, loads config, and is green in CI — with **no** provider dependency.

**Files to create/modify.**

- `go.mod`, `go.sum`, `.golangci.yml`, `Makefile`, `README.md`, `.github/workflows/ci.yml`, `deploy/Dockerfile`.
- `cmd/relay/main.go` — load config, build slog JSON logger, start an `http.Server` with explicit timeouts and graceful shutdown, mount `/healthz`.
- `internal/config/config.go` — `Load(ctx)`, `Validate()`, defaults; secret-manager interface stub (real wiring in Phase 6).
- `internal/observability/logger.go` — `slog.NewJSONHandler` setup.
- `internal/httpapi/server.go`, `internal/httpapi/router.go`, `internal/httpapi/health.go` — router, `/healthz`.
- `internal/httpapi/errors.go` — the standard error body (spec §5.5) and a `writeError(w, status, code, msg, requestID)` helper.

**Implementation notes.**

- Server timeouts are **mandatory** (zero = no timeout): `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes` (reference §3.3, spec §11). Body size cap ~16 KiB.
- Graceful shutdown: on SIGTERM/SIGINT call `srv.Shutdown(ctx)` with a bounded context (spec §14.2).
- `/healthz` returns `200 {"status":"ok"}`, unauthenticated, no dependency checks (spec §5.3).
- Error body shape is fixed (spec §5.5):
  ```json
  { "error": { "code": "string_code", "message": "human readable", "request_id": "01JRELAY..." } }
  ```
  Every request gets a ULID `request_id`, echoed as the `X-Request-Id` response header.

**Tests.**

- Unit: `config.Validate` rejects missing required fields; `writeError` emits exact JSON + `X-Request-Id`.
- Unit: `/healthz` returns 200 with the exact body.
- CI: `make lint`, `make build`, `make test` green; `migrate-check` job present (no migrations yet → no-op pass).

**Acceptance criteria.**

- [ ] `make build` produces `relay` and `relayctl` binaries.
- [ ] `make lint` and `make test` pass in CI with zero provider credentials.
- [ ] `cmd/relay` boots, serves `/healthz` 200, and shuts down gracefully on SIGTERM.
- [ ] Every response carries `X-Request-Id`; errors use the §5.5 shape.

---

### Phase 1 — Storage, migrations, `relayctl` admin CLI  (size: L)

**Goal.** The full PostgreSQL schema, a migration runner, the shared data-access package, and a working `relayctl` that can provision accounts/keys/allowlists and read op-logs — all without touching providers.

**Files to create/modify.**

- `migrations/sql/<ts>_init_relay_schema.sql` — exact schema from spec §8.1: `relay_accounts`, `relay_api_keys`, `relay_apns_allowlist`, `relay_fcm_allowlist`, `relay_op_logs`, plus indexes (`relay_api_keys_prefix_uidx`, `relay_api_keys_account_idx`, `relay_op_logs_account_time_idx`, `relay_op_logs_time_idx`).
- `internal/store/postgres.go` — `pgxpool.New(ctx, dsn)`, `Ping`, migration runner hook.
- `internal/store/redis.go` — `go-redis` client + `PING` (used Phase 2).
- `internal/accounts/` — data-access: `CreateAccount`, `ListAccounts`, `DisableAccount`, `IssueKey`, `ListKeys`, `RevokeKey`, `LookupKeyByPrefix`, `SetApnsAllowlist`, `SetFcmAllowlist`, `GetAllowlists`. Used by both `httpapi` and `relayctl`.
- `internal/accounts/keys.go` — key generation + hashing (see notes).
- `internal/oplog/oplog.go` — `Write(ctx, entry)` to `relay_op_logs`; `TokenHash(token) string` (SHA-256 hex).
- `cmd/relayctl/main.go` + subcommands per spec §5.6 table.

**Implementation notes.**

- **API-key format & hashing** (spec §8.2, reference §4.1):
  - Format `rk_<env>_<base62/base64url>`; env ∈ {`live`,`test`}. Random secret = 32 bytes from `crypto/rand`.
  - **Prefix** = `rk_<env>_<first 6–8 chars of random>`, non-secret, stored cleartext, uniquely indexed.
  - Store `key_hash = HMAC-SHA256(pepper, secret)` (32 bytes). Never store plaintext. Fast keyed hash is correct for 256-bit random keys (do **not** use argon2id here).
  - `relayctl key issue` prints the full `rk_…` **exactly once** to stdout; only hash+prefix persist (spec §8.3).
- `relayctl` connects directly to Postgres with a privileged DSN held by operators (no public admin API; spec §5.6). Each mutating command writes an `admin.*` op-log with operator identity (OS user / `--actor`) and never the secret token (spec §5.6).
- Allowlist commands replace the set atomically (UPSERT/DELETE in one tx) for `relay_apns_allowlist` / `relay_fcm_allowlist`.
- Official allowlist config values to seed in examples/tests (allowlist config, **not** the contract — spec §8.4): APNs topics `com.continuum.app.ios`, `com.continuum.app.tvos`, `com.continuum.app.macos`; FCM project `continuum-prod-android`, package `com.continuum.app.android`.

**`relayctl` command surface (spec §5.6 / §13.3).**

| Command | Effect |
|---|---|
| `relayctl account create --name <label> [--note <text>]` | INSERT `relay_accounts`; print `account_id` |
| `relayctl account list [--active]` | SELECT accounts + key count + last activity |
| `relayctl account disable --account <id>` | `status='disabled'` |
| `relayctl key issue --account <id> [--env live\|test] [--expires <dur>]` | INSERT key; print `rk_…` once |
| `relayctl key list --account <id>` | SELECT (prefix/created/last_used/revoked; never secret) |
| `relayctl key revoke --key-prefix <prefix>` | `revoked_at=now()` |
| `relayctl allowlist apns set --account <id> --topics a,b,c` | replace APNs allowlist |
| `relayctl allowlist fcm set --account <id> --project <p> --packages a,b` | replace FCM allowlist |
| `relayctl allowlist show --account <id>` | print effective allowlists |
| `relayctl logs --account <id> [--since …] [--outcome rejected]` | query `relay_op_logs` (redacted) |
| `relayctl logs --gc` | retention GC: delete success rows >14d, failure/reject rows >90d (Phase 5; cron-schedulable) |
| `relayctl ping-upstream --provider apns\|fcm --env sandbox` | credential-only health check; flips `relay_credential_healthy` (Phase 3/4/6) |

**Tests.**

- Integration (PG via docker-compose): `make migrate-up` applies cleanly on a fresh DB; `make migrate-status` lists state.
- Unit/integration: `IssueKey` → `LookupKeyByPrefix` round-trips; hash is HMAC-SHA256; plaintext never stored.
- Integration: full `relayctl` flow — create account → issue key → set allowlists → list/show; `admin.*` op-logs written without secrets.
- Unit: `oplog.TokenHash` is stable SHA-256 hex; op-log entry rejects/strips raw token.

**Acceptance criteria.**

- [ ] Migrations apply on a fresh DB and match spec §8.1 exactly (column names/types/constraints/indexes).
- [ ] `relayctl` can provision an account, issue a key (printed once), set both allowlists, and read redacted logs.
- [ ] No table stores tokens, content, or identity; `relay_op_logs` holds only `token_hash`, never raw tokens.
- [ ] Key hash is HMAC-SHA256(pepper, secret); constant-time compare verified in a unit test.

---

### Phase 2 — Auth middleware, rate limit, idempotency  (size: L)

**Goal.** The full request-path middleware chain (auth → rate limit → idempotency → decode/validate/allowlist) wired in front of stub send handlers that return a deterministic fake upstream result. No real provider yet.

**Files to create/modify.**

- `internal/httpapi/middleware_auth.go` — bearer parse, prefix lookup, constant-time compare, revoked/expired/disabled rejection, throttled `last_used_at`, per-IP anti-brute-force.
- `internal/ratelimit/limiter.go` + `ratelimit/bucket.lua` — Redis Lua token bucket (per-account + coarse per-token).
- `internal/httpapi/middleware_ratelimit.go` — applies the limiter, emits `429` + `Retry-After`.
- `internal/idempotency/store.go` + `idempotency/idem.lua` (if needed) — `SET NX` lock, replay, 409, 422.
- `internal/httpapi/middleware_idempotency.go` — wraps send handlers.
- `internal/httpapi/decode.go` — `DisallowUnknownFields` decoder + field validation + allowlist enforcement.
- `internal/httpapi/apple.go`, `internal/httpapi/fcm.go` — handler skeletons returning a fake `accepted` result (real upstream in Phase 3/4).

**Implementation notes.**

- **Auth** (spec §8.2): parse `Authorization: Bearer rk_<env>_<rest>` (malformed → `401 unauthorized`); derive `key_prefix`; `SELECT … WHERE key_prefix=$1` (unique index); reject if no row / revoked / expired / account disabled (do not distinguish reasons to caller — all `401 unauthorized`); `subtle.ConstantTimeCompare(HMAC(pepper, presented), key_hash)`; throttle `last_used_at` to ≤1/min/key; rate-limit auth failures per resolved client IP (see client-IP resolution below).
- **Constant-time auth path (no enumeration oracle).** On a missing/revoked/expired row, **still** perform a dummy `HMAC-SHA256` against a fixed decoy hash with `subtle.ConstantTimeCompare` before returning the uniform `401`, so "unknown prefix" is not measurably faster than "known prefix, wrong secret." Keep the fine-grained failure reason (`unknown_prefix` vs `hash_mismatch`) in **internal** logs/metrics only — never in a response and never exposed to an untrusted `/metrics` scraper.
- **Client-IP resolution / trusted proxy (security-critical).** The relay sits behind a TLS load balancer, so Go's `http.Request.RemoteAddr` is the LB address, not the caller. Derive the client IP from `RemoteAddr` **unless** the immediate peer is in an **allowlisted trusted-proxy CIDR list** (a config value), in which case take the **right-most untrusted hop** from the LB-set forwarding header. **Caller-supplied `X-Forwarded-For`/`X-Real-IP` headers are ignored** — trusting them would let an attacker spread brute-force across unlimited fake IPs (defeating the per-IP throttle) and poison `egress_ip` in op-logs. Both the per-IP auth-failure limiter and the logged `egress_ip` consume this single resolved IP. Account for shared egress (CGNAT/VPN — multiple self-hosted servers behind one IP, anticipated in spec §3.4): make the brute-force lockout **soft** (added latency / `Retry-After`), not a hard block, so a noisy/compromised neighbor cannot lock out legitimate servers sharing the IP.
- **Bounded fail-open on Redis outage.** The per-account/daily rate limit is the primary cap on a stolen-key attacker's spend (and on shared downstream APNs/FCM quota). A naive fail-open removes that cap entirely during a Redis outage. Instead, when Redis is unreachable, fall back to a conservative **in-process** per-account limiter (`x/time/rate`) so each account is still capped to a small multiple of its normal rate; the allowlist (always enforced from Postgres) remains the hard boundary. A Redis outage pages ops; the cross-replica daily quota cannot be enforced during the outage (documented limitation).
- **Rate limit** (spec §9): Redis HASH `rl:{account_id} = {tokens, last_refill_ts}` with TTL, atomic Lua (refill→check→consume in one `EVAL`). Defaults (spec §9.2): per-account ~10 req/s burst, refill 10/s with burst capacity 20; per-account daily 50,000 req/day; coarse `(account_id, token_hash)` ~1 req/3s. Over limit → `429 rate_limited` with **relative** `Retry-After` (delay-seconds, never an absolute epoch — reference §4.2). On Redis down: **bounded fail-open** (in-process per-account fallback limiter, above — not unlimited), log `rate_limit.degraded`, increment `relay_rate_limit_degraded_total` (spec §9.4).
  - **Daily quota mechanics.** Store the daily counter as a **separate Redis counter** `rl:day:{account_id}:{yyyymmdd}` with `INCR` + `EXPIRE` to the end of the window — it is **not** the per-second token bucket and **not** a Postgres column. Reset semantics: **fixed UTC calendar day** (`yyyymmdd` in UTC). During a Redis outage the daily counter, like the per-second bucket, falls back to the bounded in-process limiter and the precise cross-replica daily cap cannot be enforced (documented; pages ops). Emit `relay_rate_limited_total{kind="daily"}` and alert on accounts approaching/hitting the cap (a big-release burst can legitimately reach 50k/day; §6.4).
- **Project-level FCM circuit breaker (multi-tenant fairness).** The ~600k msgs/min FCM project quota (reference §2.7) is **shared across all relay accounts**, but the per-account limit is local to each account — one noisy account can exhaust the shared quota for everyone. Add a relay-side **global per-project outbound limiter / circuit breaker** that tracks aggregate FCM send rate against the project quota and **sheds load with `503 upstream_throttled` before hammering FCM** when near quota (the relay is the only aggregation point that can honor reference §2.7's anti-spike / smooth-traffic guidance — avoid sending within a 2-minute window of :00/:15/:30/:45). Export `relay_fcm_project_quota_headroom` and alert (§6.4).
- **Idempotency** (spec §10): require `Idempotency-Key` header on sends; missing → `400 missing_idempotency_key`. Length-limit ≤255 chars; the three-part `<delivery_id>:<device_id>:<attempt>` shape is **opaque** (not strictly parsed). Namespaced `idem:{account_id}:{key}`. `SET … NX EX <lock_ttl>` (`lock_ttl` ≈ 30s) in-flight lock:
  - NX success → process; on completion overwrite with serialized `{status, body, upstream_id, canonical_hash}` + ~24h TTL (success **and** error recorded).
  - NX fail + stored completed result → **replay** verbatim (same `apns_id`/`fcm_message_name`).
  - NX fail + still in-flight → `409 idempotency_conflict`.
  - Same key + different request payload (compare stored `canonical_hash`) → `422 idempotency_key_reuse`.
  - **Fails closed**: Redis unavailable → `503 upstream_unavailable` (never risk a double-send; spec §10.2).
  - **Lock-TTL invariant (no double-send / no clobber).** The upstream APNs/FCM call (plus HTTP/2 setup, JWT/OAuth refresh, GOAWAY reconnect) can otherwise outlast `lock_ttl`, letting a concurrent retry's NX succeed and fire a **second** upstream send. Pin the upstream call with a hard context deadline **strictly shorter than `lock_ttl`** (≈10s upstream vs 30s lock) and refuse to send upstream if the handler's own deadline has already passed the lock TTL. On completion, use a **compare-and-set** that only overwrites the stored value if it is still *this* request's in-flight marker, so a lock stolen by a concurrent request cannot have its result clobbered.
  - **Privacy invariant (no stored token).** The `canonical_hash` is computed over the validated fields with the device token **replaced by its already-computed `token_hash` (SHA-256)**; the stored idempotency value is exactly `{status, body, upstream_id, canonical_hash}` and holds **no raw token** — matching the op-log no-raw-token guarantee (NG1, spec §3.2/§12).
  - **Drain interaction.** On handler context-cancellation (e.g. a drain/shutdown that severs an in-flight send), **release or downgrade** the in-flight marker so an immediate caller retry does not get spurious `409 idempotency_conflict` for the full `lock_ttl` (see Phase 6 drain ordering and §5.7 drain test).
- **Decode/validate/allowlist** (spec §5.1/§5.2/§8.4): `DisallowUnknownFields` (unknown key → `400 unexpected_field`). Validate every field per the spec field tables (token shape, environment, mode, ID length caps, collapse-id ≤64 bytes for APNs). Allowlist check **after** auth, **before** payload build: APNs `topic` ∈ account's `relay_apns_allowlist` else `403 topic_not_allowed`; FCM `(project_id, package_name)` ∈ `relay_fcm_allowlist` else `403 project_not_allowed` / `403 package_not_allowed`. Allowlist may be a short-TTL per-account-scoped in-memory cache invalidated on `relayctl allowlist … set`.

**Middleware order (spec §4.1).**

```
TLS → auth → rate-limit → decode(DisallowUnknownFields) → validate+allowlist → idempotency → [Phase 3/4 upstream] → map → store idem result → op-log + metrics
```

**Tests.**

- Unit: bearer parse edge cases; constant-time compare; revoked/expired/disabled all map to `401`; `last_used_at` throttle.
- Unit: the auth path is **constant-time for unknown-prefix vs wrong-secret** (dummy HMAC + `ConstantTimeCompare` on a missing/revoked row) — no enumeration timing oracle; the fine-grained reason stays internal-only.
- Unit: **trusted-proxy client-IP resolution** — a forged caller `X-Forwarded-For`/`X-Real-IP` is ignored; only the LB-set header from an allowlisted trusted-proxy peer is honored; `RemoteAddr` is used otherwise.
- Integration (Redis): token bucket refill/consume math; `429` + correct relative `Retry-After`; **daily counter** (`rl:day:{account}:{yyyymmdd}`, fixed-UTC reset, `EXPIRE`) trips at 50k; **bounded** fail-open on Redis down (in-process per-account cap engaged, not unlimited).
- Integration (Redis): idempotency first/replay/409/422/missing-key matrix (spec §10.3); fail-closed `503` on Redis down; the stored value carries **no raw token** (canonical hash uses `token_hash`).
- Integration (Redis): **Redis dropped mid-request** (not just down at start) — rate-limit layer degrades to bounded fail-open; idempotency layer fails closed (`503`); a lock whose TTL expires mid-send cannot be stolen into a double-send (upstream deadline < `lock_ttl`).
- Contract: `DisallowUnknownFields` rejects every disallowed field (`title`, `body`, `image_url`, `media_id`, `username`, `server_url`, …) with `400 unexpected_field`.
- Contract: allowlist rejections produce exact codes `topic_not_allowed` / `project_not_allowed` / `package_not_allowed`.

**Acceptance criteria.**

- [ ] A request with a valid key, allowlisted topic, and `Idempotency-Key` reaches the (stub) handler and returns `accepted`.
- [ ] Duplicate idempotency key replays the identical body; in-flight → `409`; payload mismatch → `422`; missing → `400`.
- [ ] Over-limit → `429` with relative `Retry-After`; Redis-down rate-limit fails open, idempotency fails closed.
- [ ] Every unknown field and every non-allowlisted topic/project/package is rejected with the exact §5/§8 error code.

---

### Phase 3 — APNs client + `POST /v1/apple/send`  (size: L)  · gated on Apple provisioning

**Goal.** A production APNs client (cached ES256 JWT, reused HTTP/2 pool, correct reason→error mapping) and the live `/v1/apple/send` endpoint building the §5.1 payloads via `pushwire`, validated against the APNs **sandbox**.

**Files to create/modify.**

- `internal/apns/client.go` — the `APNsClient` interface + concrete impl (apns2 **or** hand-rolled per §3 decision).
- `internal/apns/jwt.go` — ES256 JWT lifecycle (cache, ~50min refresh, never <20min, eager refresh on `ExpiredProviderToken`).
- `internal/apns/transport.go` — per-team HTTP/2 pool with `ReadIdleTimeout`/`PingTimeout`; env→host selection.
- `internal/apns/errors.go` — APNs status+`reason` → caller `error.code` + retryability (spec §6.4 table).
- `internal/pushwire/apns.go` — build `private_alert` / `background_wake` `aps`+`silo` payloads and `apns-*` headers (spec §5.1).
- `internal/httpapi/apple.go` — replace the Phase 2 stub with the real send.

**Implementation notes.**

- **JWT** (spec §6.1, reference §1.1): ES256 only; header `{"alg":"ES256","kid":<10-char Key ID>}`, claims `{"iss":<10-char Team ID>,"iat":<epoch s>}`. Cache one JWT behind a mutex; regenerate on a ~50-min timer, never <20-min apart; eager regenerate + **single in-process retry** on `403 ExpiredProviderToken`; back off / slow refresh on `429 TooManyProviderTokenUpdates`. **Per-team pool** keyed by team even though v1 has one team.
- **Single-flighted JWT regeneration (stampede guard).** Under a burst of concurrent sends sharing one cached JWT, an expiry (or a `403 ExpiredProviderToken`) is observed by many in-flight goroutines at once; a naive per-goroutine regenerate-on-403 would trip `429 TooManyProviderTokenUpdates`. Regeneration must therefore be **single-flighted** (e.g. `golang.org/x/sync/singleflight` or a generation/epoch counter under the mutex): a goroutine seeing `ExpiredProviderToken` first checks whether the JWT was already refreshed since *its* request started and reuses the new token rather than triggering another refresh. Exactly **one** refresh happens per ~20-min window regardless of how many concurrent 403s arrive.
- **Endpoint** (spec §6.2): `production` → `https://api.push.apple.com`; `sandbox` → `https://api.sandbox.push.apple.com`. Both `api.sandbox.push.apple.com` and the older alias `api.development.push.apple.com` are **valid, equivalent sandbox hosts that still resolve** — we standardize on `api.sandbox.push.apple.com` (the current canonical name and `apns2` v0.25.0's `HostDevelopment`); `02`'s use of `api.development.*` is not a discrepancy to resolve, just the older alias. The external contract `"environment":"sandbox"` is unchanged. The upstream hosts are **hardcoded constants never derived from request input**. Port 443, 2197 fallback.
- **Connection** (spec §6.3, reference §3.3): one client/pool per process per env per team; never per push. Non-zero `ReadIdleTimeout`(~15s) + `PingTimeout`(~15s). Do not hardcode `SETTINGS_MAX_CONCURRENT_STREAMS`. Handle `GOAWAY` (drop+reconnect). Ensure system trust store includes USERTrust RSA (default in OS/Go pools).
- **Headers** (spec §5.1 table): `apns-topic` = allowlisted topic; `apns-push-type` = `alert` (private_alert) / `background` (background_wake); `apns-priority` = `10` / `5` (background **must** be 5; 10 is an error — reference §1.4.1); `apns-collapse-id` = `collapse_id` if present (≤64 bytes); `apns-id` omitted (Apple generates, echoed back). **`apns-expiration` ships with a finite default** (configurable) rather than 0/omitted: a few hours for `private_alert`, a short TTL for `background_wake`. Omitting it makes Apple store-and-retry an undelivered push for **up to 30 days** (reference §1.4) — for a notification whose content is fetched live on wake, a wake fired days later is useless or confusing. Mirror this on FCM with a finite `android.ttl` (default is otherwise ~4 weeks) for cross-provider parity. (Resolved open Q#5; the exact default values want product input but v1 ships a sane non-30-day default, not an omission.)
- **Payloads** (`pushwire`, spec §5.1): `private_alert` → `aps.alert` with `title-loc-key=SILO_NOTIFICATION_TITLE`, `loc-key=SILO_NOTIFICATION_GENERIC_BODY`, `sound=default`, plus `silo` dict (`v:1`, `wake:"notifications.changed"`, `server_device_id`, `delivery_id`); add `aps.badge` only if `badge` is present, non-null, and within a **bounded range (0–9999)** — out-of-range → `400 invalid_field` (it is the one numeric field that escapes the opaque-ID discipline, so it is bounded rather than copied verbatim). `background_wake` → `aps.content-available:1` + `silo` dict. Uncompressed JSON, far under 4 KB; defensive `400 payload_too_large` if it ever exceeds.
- **Response → caller** (spec §5.1): `200` → `{ "request_id", "apns_id" (Apple's apns-id header), "status":"accepted" }`. Error mapping per spec §6.4 (e.g. `BadDeviceToken`/**`InvalidToken`**/`DeviceTokenNotForTopic`/`MissingDeviceToken` → `422 bad_device_token`; `Unregistered`/`ExpiredToken` → `410 unregistered` with `timestamp` passed through in `error.message`; `403 InvalidProviderToken`/etc → `502 upstream_auth_error` + ops alert; `429 TooManyRequests` → `429 rate_limited` + `Retry-After`; 5xx → `503 upstream_unavailable`). **`InvalidToken` is treated equivalently to `BadDeviceToken` (token-purge, non-retryable)** — Apple has migrated some responses from `BadDeviceToken` to `InvalidToken`, and the `02` contract requires treating them equivalently; omitting it would let a dead device fall through to the catch-all and never be disabled. Add a **default/fallback rule**: any unrecognized 400/403/410 `reason` maps to a safe non-retryable `502 upstream_error` rather than being dropped. **Connection-severed sends** — a request whose HTTP/2 stream is terminated by `GOAWAY` or any connection reset **before** a final APNs response arrives → `503 upstream_unavailable` (retryable), the same as a network timeout; the relay does **not** transparently retry it (the caller's outbox does), preserving at-most-once semantics under the idempotency key. Tag the op-log outcome / metric label `connection_severed` so these are distinguishable. **Environment/token mismatch caveat:** a `sandbox`/`production` mismatch (e.g. a prod token sent with `environment=sandbox`) presents upstream as `BadDeviceToken` → `bad_device_token`, which instructs the caller to purge a perfectly good token; emit a distinct op-log/metric so a systemic misconfig (many `bad_device_token` from one account) is distinguishable from genuinely dead tokens. Network/timeout to APNs → `503 upstream_unavailable`. The relay does **not** retry transient upstream errors across caller requests (the caller's outbox does); the only in-process retry is the JWT-expiry one (single-flighted — see JWT note below).

**Tests.**

- Unit: JWT signs valid ES256 with correct header/claims; refresh-timer logic (≥20min, ≤60min, eager-on-expired); single retry on `ExpiredProviderToken`.
- Concurrency: force JWT expiry under a burst of concurrent sends; assert **exactly one** regeneration (single-flight) and **zero** `429 TooManyProviderTokenUpdates`.
- Unit (`pushwire`): golden-file payloads + headers for `private_alert` and `background_wake`, with and without `badge`/`collapse_id` (byte-exact vs spec §5.1). **Same golden files mirrored into `silo-server`.**
- Unit: every row of the spec §6.4 reason→error table maps correctly (table-driven), **including `InvalidToken` → `bad_device_token`** (equivalent to `BadDeviceToken`) and the **default/fallback** rule (unrecognized reason → `502 upstream_error`, non-retryable).
- Fake APNs upstream injecting a **`GOAWAY` / mid-stream reset** before a final response → caller gets `503 upstream_unavailable`, the op-log/metric is tagged `connection_severed`, and there is **no lost or double send** (the relay does not transparently retry).
- Integration (**Apple sandbox**, behind `-tags=integration`, requires `.p8`): real send to `api.sandbox.push.apple.com` returns 200 + `apns_id`; an intentionally bad token returns the expected `bad_device_token`/`unregistered` mapping.
- Integration: `relayctl ping-upstream --provider apns --env sandbox` signs+connects without sending content; optionally captures the sandbox-only `apns-unique-id` response header (reference §1.6) into the ping output for Push-Notifications-Console delivery-log correlation during client QA.

**Acceptance criteria.**

- [ ] `/v1/apple/send` builds the exact §5.1 payloads/headers and returns `{request_id, apns_id, status:"accepted"}` on success against sandbox.
- [ ] Background pushes always send `apns-priority: 5` and `apns-push-type: background`.
- [ ] JWT is cached, reused, and refreshed on the ~50-min cadence; `ExpiredProviderToken` triggers one regenerate+retry; concurrent 403s yield exactly one single-flighted regeneration.
- [ ] Every spec §6.4 mapping is covered by a passing table-driven test, **including `InvalidToken` and the unrecognized-reason fallback**; a `GOAWAY`/connection-severed send maps to `503 upstream_unavailable` with no double-send; the `pushwire` golden files match across both repos.

---

### Phase 4 — FCM client + `POST /v1/fcm/send`  (size: L)  · gated on Firebase provisioning

**Goal.** A production FCM HTTP v1 client (cached OAuth2 token, tuned HTTP/2, correct status→error mapping) and the live `/v1/fcm/send` endpoint building the §5.2 **data-only** payloads via `pushwire`, validated with FCM `validateOnly:true`.

**Files to create/modify.**

- `internal/fcm/client.go` — the `FCMClient` interface + concrete impl (`x/oauth2/google` path per §3).
- `internal/fcm/oauth.go` — `CredentialsFromJSONWithType(ctx, json, google.ServiceAccount, "https://www.googleapis.com/auth/firebase.messaging")` → cached/auto-refreshing `TokenSource`, one per project.
- `internal/fcm/transport.go` — tuned HTTP/2 client; endpoint `POST https://fcm.googleapis.com/v1/projects/{PROJECT_ID}/messages:send`.
- `internal/fcm/errors.go` — FCM `error.status` → caller `error.code` + retryability + Retry-After honoring (spec §7.4).
- `internal/pushwire/fcm.go` — build data-only `message` + `android` config (spec §5.2).
- `internal/httpapi/fcm.go` — replace the Phase 2 stub with the real send.

**Implementation notes.**

- **OAuth2** (spec §7.1, reference §2.2): least-privilege scope `firebase.messaging`; `TokenSource` caches + auto-refreshes (~1h tokens; never mint per request). On GCP prefer keyless Workload Identity / attached SA; off-GCP load SA JSON from the secret manager (open Q#4). One token source per Firebase project.
- **Endpoint** (spec §7.2): global host only — no regional endpoints (reference §2.3). The host `fcm.googleapis.com` is a **hardcoded constant never derived from request input**. `{PROJECT_ID}` is the allowlist-checked `project_id`, and `project_id`/`package_name` are validated against a **strict charset** (e.g. `^[a-z0-9-]+$` / reverse-DNS) **before** allowlist lookup and URL interpolation, so an allowlist row can never introduce a path-traversal or host-override (no SSRF / path injection).
- **Payload — data-only** (`pushwire`, spec §5.2): `message.token` = caller token; `message.data` is a **fixed, closed set of string-valued keys only** (`"v":"1"`, `"wake":"notifications.changed"`, `"server_device_id"`, `"delivery_id"`) — no caller-controlled key or value beyond the opaque IDs; **no `notification` block**; `message.android.priority` = `"HIGH"` (private_data) / `"NORMAL"` (background_wake); `message.android.collapseKey` set only if the caller sent `collapse_key`. **Field-casing reconciliation (reference §2.4):** the relay POSTs raw JSON to the real FCM HTTP v1 REST endpoint, whose AndroidConfig field is camelCase **`collapseKey`** (FCM v1 rejects unknown fields with `INVALID_ARGUMENT`, so a literal `collapse_key` inside `android` would fail every collapsible send). The **inbound caller request body field stays `collapse_key`** per `03`; only the relay-built upstream JSON uses `collapseKey`. Likewise `android.priority` is sent as canonical uppercase `"HIGH"`/`"NORMAL"` (reference §2.4 lists these as the enum values, with lowercase also accepted; the relay sends the canonical uppercase to be robust against future enum tightening). Honor the `03` contract's `priority: high`-equivalent (`"HIGH"`) for `private_data` (do not second-guess — spec §5.2 reconciliation note). Set a finite `android.ttl` (default ~4 weeks otherwise) for parity with the APNs `apns-expiration` default (above). Never set `topic`/`condition`. ≤4096 bytes. Use `validateOnly:true` (camelCase REST field — reference §2.5) only for the `relayctl` test path, never production.
  - **`badge` has no data-only FCM home.** The relay-built FCM payload is strictly data-only (no `notification` block), so a caller-provided `badge` has nowhere legitimate to go. **Drop `badge` from the FCM accepted schema** (it is disabled-by-default and would otherwise have to be smuggled into the closed `data` map, weakening the allowlist). On the APNs side, bound `badge` to a sane range (e.g. 0–9999) and reject otherwise with `400 invalid_field` — it is the one numeric field that escapes the opaque-ID discipline.
  - **Collapse-key coalescing is best-effort (reference §2.7).** FCM stores at most **4 distinct `collapseKey`s per device** while offline; a 5th evicts one nondeterministically. Since `collapse_key` is a per-series HMAC and a device may follow >4 series, collapsible coalescing is best-effort and excess distinct keys evict nondeterministically — acceptable because the durable inbox + sync (the caller's outbox) covers any dropped collapsible wake. APNs has no equivalent documented per-device collapse-id cap (note the asymmetry).
- **Response → caller** (spec §5.2): `200` → `{ "request_id", "fcm_message_name" (FCM v1 `name`), "status":"accepted" }`. Error mapping per spec §7.4 (e.g. `INVALID_ARGUMENT` → `422 invalid_argument`; `SENDER_ID_MISMATCH` → `403 sender_id_mismatch`; `UNREGISTERED` → `410 unregistered`; `QUOTA_EXCEEDED` → `429 rate_limited` honoring upstream `Retry-After` else default **60s**; `INTERNAL`/`UNAVAILABLE` → `503 upstream_unavailable` honoring `Retry-After` on 503; `UNSPECIFIED_ERROR` → `502 upstream_error`, no auto-purge). Classify on `error.status`/`FcmError.errorCode`, not the bare HTTP code. **Retry-After** forwarded to the caller as relative delay-seconds (reference §4.2). Add a **default/fallback rule** so any unrecognized FCM `error.status`/`errorCode` maps to a safe non-retryable `502 upstream_error` (never an auto-purge), rather than being dropped.
  - **`THIRD_PARTY_AUTH_ERROR` (401) — re-classified (do NOT page as a relay-credential failure).** Per Firebase's error-codes page this means an invalid/missing **APNs cert/key or Web Push auth credential** and is returned only for iOS-via-APNs or Web Push registration tokens. `/v1/fcm/send` sends **only** Android registration tokens (`message.token`, package `com.continuum.app.android`, data-only), so a legitimate Android send can never produce `THIRD_PARTY_AUTH_ERROR`. If it ever appears, treat it as a **per-token/registration anomaly** → `502 upstream_error`, surface for investigation, and do **not** page on-call as a relay `.p8`/SA misconfig. The relay's **actual** OAuth2/service-account credential failures surface as a non-200 from `oauth2.googleapis.com/token` at mint time, or HTTP `401 UNAUTHENTICATED` / `403 PERMISSION_DENIED` (the `status`, not an `FcmError` code) on `messages:send` — **those** map to `502 upstream_auth_error` + ops alert.
  - **Per-device rate exceedance (`DeviceMessageRateExceeded`, reference §2.7).** The Android per-device caps (240/min, 5,000/hr) are **distinct** from project-level `QUOTA_EXCEEDED` and surface defensively as a quota/`INVALID_ARGUMENT`-adjacent signal whose exact code/string is not firmly documented; detect on `error.status`/`errorCode` and map to a retryable `429 rate_limited` with a conservative default `Retry-After`, **not** a token purge.
  - **`UNREGISTERED` caller-facing 410 is an intentional cross-provider normalization.** FCM natively returns `UNREGISTERED` as HTTP **404**; the relay remaps it to caller `410 unregistered` so the caller's device-disable ("purge dead token") logic is uniform with the APNs path (which natively uses 410). This is a deliberate divergence from FCM's native status, not a bug.

**Tests.**

- Unit: token source caches; no per-request mint; refresh margin.
- Unit (`pushwire`): golden-file data-only payloads for `private_data` and `background_wake`, with/without `collapse_key` (byte-exact vs spec §5.2; **all data values strings**; **no `notification` block**). Mirrored into `silo-server`.
- Unit: every row of the spec §7.4 status→error table (table-driven), including `Retry-After` honoring + 60s default, the unrecognized-`error.status` fallback (`502 upstream_error`), the per-device-rate (`DeviceMessageRateExceeded`) → `429 rate_limited` (no purge), and `THIRD_PARTY_AUTH_ERROR` → `502 upstream_error` (per-token anomaly, **not** paged as relay-credential).
- Unit/contract: the FCM upstream JSON uses camelCase `collapseKey`, uppercase `priority` (`"HIGH"`/`"NORMAL"`), and `validateOnly`; `badge` is rejected from the FCM schema; the `data` map is the closed key set (no extra keys).
- Integration (**FCM `validateOnly:true`**, `-tags=integration`, requires SA JSON): a valid request validates OK; a malformed token surfaces `invalid_argument`/`unregistered` mapping.
- Integration: `relayctl ping-upstream --provider fcm --env sandbox` mints a token + does a `validateOnly` send.

**Acceptance criteria.**

- [ ] `/v1/fcm/send` builds the exact §5.2 data-only payload (string values, no `notification`) and returns `{request_id, fcm_message_name, status:"accepted"}`.
- [ ] OAuth2 token is cached/refreshed, never minted per request; one source per project.
- [ ] Every spec §7.4 mapping is covered (incl. the unrecognized-status fallback, per-device-rate, and `THIRD_PARTY_AUTH_ERROR` as a non-paging per-token anomaly); `QUOTA_EXCEEDED`/`UNAVAILABLE` forward `Retry-After` (default 60s on 429).
- [ ] The upstream FCM JSON uses `collapseKey` (camelCase), `"HIGH"`/`"NORMAL"` priority, `validateOnly` (camelCase), a finite `android.ttl`, and the closed `data` key set; `badge` is not accepted on the FCM path.
- [ ] `validateOnly` test path works; production sends never set `validateOnly`.

---

### Phase 5 — Observability, redaction, metrics  (size: M)

**Goal.** Production-grade redacted logging, op-log persistence, the full Prometheus metric catalog, and `/readyz`/`/metrics` wired and network-scoped.

**Files to create/modify.**

- `internal/observability/redact.go` — redaction middleware (strips `Authorization`/bearer, raw key, raw token, JWT, OAuth2 token, payload bodies).
- `internal/observability/metrics.go` — Prometheus registry + the spec §13.2 catalog.
- `internal/httpapi/health.go` — `/readyz` (PG `SELECT 1`, Redis `PING`; APNs and FCM reported **per-provider**, not as a single combined gate) and `/metrics` (`promhttp.HandlerFor`), both bound to the internal network.
- `internal/oplog/gc.go` + `cmd/relayctl` `logs --gc` subcommand (or a background sweeper in `cmd/relay`) — the retention GC deliverable; plus the §8.1 partitioning DDL (or a documented partition/offload decision).
- Wire `oplog.Write` into every send path with the redacted subset.

**Implementation notes.**

- **Redaction enforced in middleware** so no code path can leak (spec §11/§12/§13.1). Never log: bearer header, raw API key (log prefix or hash only), raw device tokens (log `SHA-256` truncated hash only), payload bodies, relay JWT, OAuth2 tokens. Do log: `request_id`, `account_id`, `status_code`, `error_code`, upstream reason, `token_hash`, `egress_ip`, `latency_ms`.
- **Op-log retention is a concrete Phase 5 deliverable** (spec §8.1/§13.1): every send writes one `relay_op_logs` row (redacted by construction). Ship a real GC — **add `relayctl logs --gc`** (to the §5.6 command table and Phase 1/5 file lists) **or** a built-in background sweeper in `cmd/relay` — with a definite retention query (`DELETE` success rows >14d, failure/reject rows >90d) on a schedule with an owner. Either add **time-partitioning DDL** to the §8.1 schema or document an explicit partition/offload decision here (do **not** defer it to an open question). Add a disk-usage / `relay_op_logs` table-growth alert (at 50k/day/account the table grows unbounded without GC — a classic "works for months then the disk fills" surprise).
- **Metrics catalog** (spec §13.2): `relay_requests_total`, `relay_request_duration_seconds`, `relay_upstream_requests_total`, `relay_upstream_duration_seconds`, `relay_auth_failures_total`, `relay_rate_limited_total` (incl. `kind="daily"`), `relay_rate_limit_degraded_total`, `relay_idempotency_total`, `relay_apns_jwt_refresh_total`, `relay_fcm_token_refresh_total`, `relay_redis_up`, `relay_pg_up`, plus `relay_provider_healthy{provider=...}`, `relay_credential_healthy{provider=...}`, and `relay_fcm_project_quota_headroom`. **Keep labels low-cardinality** (no token, no `request_id`).
- **`/readyz` — per-provider, not all-or-nothing.** Return `200` only when the **core deps** (PG + Redis) are reachable, and report **per-provider** APNs/FCM health separately in `checks` (a `relay_provider_healthy{provider}` gauge). A single provider's credential/init failure must **not** down the whole replica (that would pull a healthy APNs replica from the LB just because FCM creds are temporarily unresolvable, killing Apple push too); instead the send handler for an un-initialized/degraded provider returns `503 upstream_unavailable` for **that provider only**. Surface per-provider circuit-breaker state so operators see "APNs degraded, FCM healthy" rather than a binary not-ready. Body shape: `503 {"status":"not_ready","checks":{...}}` only when a core dep is down (spec §5.4).
- `/readyz` and `/metrics` bound to the internal/scrape network; only `/v1/*` and `/healthz` are public (spec §11/§14.1).

**Tests.**

- Unit: redaction middleware drops every disallowed field even when handlers attempt to log them (assert no token/bearer/JWT/OAuth2 string appears in captured log output).
- Unit: a success and a failure path each produce exactly one op-log row with the redacted subset and a `token_hash` (never raw token).
- Unit: metric counters increment with correct labels for first/replay/conflict idempotency, auth-failure reasons, rate-limit kinds, JWT/token refresh results.
- Integration: `/readyz` flips to `503` when PG or Redis is down; `/metrics` exposes the full catalog.

**Acceptance criteria.**

- [ ] No log line or op-log row can contain a raw token, bearer header, JWT, OAuth2 token, or payload body (asserted by tests).
- [ ] All spec §13.2 metrics are exported with low-cardinality labels.
- [ ] `/readyz` reflects real dependency + credential health; `/metrics` and `/readyz` are not publicly routable.

---

### Phase 6 — Hardening, load test, deploy  (size: L)  · gated on provisioning + secret manager

**Goal.** Production hardening (real secret-manager wiring, TLS, anti-abuse), a hundreds-of-users burst load/pacing test, idempotency-under-concurrency proof, container build, and rollout.

**Files to create/modify.**

- `internal/config/secrets_*.go` — real secret-manager integration (KMS/Vault/cloud secret manager); load `.p8`, SA JSON, pepper, DSNs at startup into memory; optional SIGHUP re-read.
- `deploy/Dockerfile` (distroless/minimal), deploy manifests, TLS/HSTS config.
- `test/load/` — k6/vegeta burst scenario + concurrency idempotency test harness.
- Alerting rules (Prometheus → alertmanager / SLO doc).

**Implementation notes.**

- **Secrets** (spec §11, reference §4.5): credentials loaded once at startup from the secret manager into memory; never on disk in the image/repo, never logged, never returned by any endpoint. On GCP prefer keyless FCM (Workload Identity). Rolling restart picks up rotated secrets.
- **TLS** (spec §11/§14.1): HTTPS/HTTP-2 only, TLS 1.2+ (prefer 1.3), HSTS on `relay.silo.app`. Terminate at LB or in-process.
- **Anti-abuse** (spec §8.2/§11): per-IP auth-failure rate limiting keyed on the **trusted-proxy-resolved client IP** (caller-supplied `X-Forwarded-For`/`X-Real-IP` ignored — see Phase 2 client-IP resolution; the trusted-proxy CIDR list is config); **soft** lockout (latency / `Retry-After`) to avoid CGNAT/VPN collateral; body size cap; explicit timeouts; graceful shutdown. Document a response runbook for the key-guessing alert.
- **Credential-health validation & rotation runbook** (spec §4.4/§14.3). Add a **startup + periodic synthetic credential check** that proves the loaded credential actually authenticates upstream — not just that a JWT signs / a token mints locally (which `/readyz` already checks). For FCM, a `validateOnly:true` send; for APNs, an auth-exercising send to a known-bad token that still distinguishes an auth-layer `403` (`InvalidProviderToken`) from a token-layer `400` (`BadDeviceToken`). Wire the result into a `relay_credential_healthy{provider=...}` gauge and into `/readyz`'s per-provider health (below). Schedule `relayctl ping-upstream` as a cron for continuous health, and **alert on the first `upstream_auth_error`** (§6.4) — Apple actively revokes leaked `.p8` keys and SA keys/bindings can be rotated out from under the relay. Document the **rotation runbook** (overlap window, dual-key support — APNs allows multiple keys and the relay pools by team).
- **Graceful-shutdown drain ordering** (spec §14.2). On `SIGTERM`: (1) flip `/readyz` to `503` so the LB stops routing **new** requests; (2) keep serving in-flight handlers; (3) bound `srv.Shutdown(ctx)` to **less than `lock_ttl`** so any marker left by a killed-mid-flight handler expires quickly — **and** release/downgrade the in-flight idempotency marker on handler context-cancellation (Phase 2), so a drained-but-incomplete send frees its lock immediately rather than forcing the caller's retry to eat a 30s `409 idempotency_conflict`. A clean drain must leave **no orphaned in-flight idempotency markers**; the relationship between the shutdown grace period and `lock_ttl` is explicit.
- **Load/pacing test** (spec §13 / testing strategy §5 below): simulate a hundreds-of-users burst (e.g. 300–500 concurrent device sends), assert the relay stays under the per-account cap, returns correct `429` + relative `Retry-After`, and one well-tuned upstream connection sustains the throughput (reference §1.3 — thousands/sec). Verify the FCM smooth-traffic guidance does not trip quota (reference §2.7).
- **Idempotency under concurrency**: fire N concurrent requests with the **same** `Idempotency-Key`; assert **at most one** upstream send (count via a fake upstream), the rest replay or `409`.
- **FCM quota-storm shedding**: when the shared project quota is exhausted, the relay-side project circuit breaker sheds with `503 upstream_throttled` before forwarding (Phase 2 / §6.4), rather than hammering FCM into a 429 stampede.
- **HA** (spec §14.2): multiple stateless replicas behind the LB; rolling deploys; kill-safety (a drained/killed replica is covered by the caller's outbox retry). Relay-down delays, never loses.

**Tests.**

- Load: hundreds-of-users burst (latency p50/p95/p99, error rate, `429` correctness).
- Concurrency: same-key idempotency → at-most-once upstream (assert against a counting fake).
- Security: secret-manager load path; no secret in image (`docker history`/scan); TLS config (min version, HSTS).
- Security: forged caller `X-Forwarded-For`/`X-Real-IP` does **not** change the resolved client IP / `egress_ip` (only the trusted-proxy header from an allowlisted peer is honored); per-IP throttle cannot be bypassed.
- Drain: rolling-restart drains in-flight requests via `srv.Shutdown` without dropping a completed send, `/readyz` flips to `503` before draining, and **no orphaned in-flight idempotency markers** remain afterward (an immediate caller retry does not get a spurious `409`).
- Resilience: FCM quota-exhaustion → relay sheds with `503 upstream_throttled` (project circuit breaker) instead of forwarding into a 429 storm.
- Credential health: the synthetic startup/periodic check flips `relay_credential_healthy` and `/readyz` when the loaded credential is rejected upstream (auth-layer `403`), distinct from a token-layer `400`.

**Acceptance criteria.**

- [ ] Secrets load exclusively from the secret manager; image/repo scans show no embedded `.p8`/SA JSON/pepper.
- [ ] A hundreds-of-users burst is handled within the per-account caps with correct `429`/`Retry-After`; one upstream connection sustains the load.
- [ ] Concurrent identical-key sends produce **at most one** upstream call.
- [ ] A rolling restart drains in-flight requests, flips `/readyz` to `503` first, and leaves **no orphaned in-flight idempotency markers**.
- [ ] The synthetic credential check gates `/readyz`/`relay_credential_healthy` on real upstream acceptance; the first `upstream_auth_error` pages ops; the rotation runbook is documented.
- [ ] Forged caller forwarding headers cannot poison `egress_ip` or bypass the per-IP throttle.
- [ ] TLS 1.2+ enforced, HSTS set; `/metrics`+`/readyz` internal-only; staging deployment validated before production rollout.

---

## 5. Testing Strategy

### 5.1 Layers

| Layer | What it covers | Provider creds? |
|---|---|---|
| **Unit** | Field validation, JWT/token lifecycle math, key hashing/compare, error-table mappings, redaction, metric labels | No |
| **Contract** | Exact `02`/`03` request/response shapes: `DisallowUnknownFields` rejects every disallowed field; success/error bodies are byte-exact; `pushwire` golden files | No |
| **Integration** | Real upstream behavior: APNs **sandbox** sends; FCM **`validateOnly:true`** validation; PG migrations; Redis rate-limit/idempotency | APNs `.p8` / FCM SA (sandbox) |
| **Redaction assertions** | No raw token/bearer/JWT/OAuth2/payload ever reaches a log or op-log | No |
| **Load / pacing** | Hundreds-of-users burst; per-account cap + `429`/`Retry-After`; upstream connection reuse sustains throughput | Optional (fake upstream) |
| **Idempotency-under-concurrency** | Same key, many concurrent requests → at-most-once upstream send | No (fake upstream) |
| **Failure-mode** | APNs `GOAWAY`/mid-stream reset mapping (no lost/double send); JWT-expiry single-flight (one regen, zero `TooManyProviderTokenUpdates`); Redis dropped mid-request (idempotency fail-closed, rate-limit bounded fail-open); graceful-drain leaves no orphaned idempotency lock; FCM quota-exhaustion shedding | No (fakes) |

### 5.2 Contract tests against the 02/03 shapes

For each endpoint, a golden corpus of requests/responses derived from spec §5.1/§5.2:

- **Valid** `private_alert` / `background_wake` (APNs) and `private_data` / `background_wake` (FCM) requests → assert the built upstream payload + headers are **byte-exact** vs the spec (the `pushwire` golden files).
- **Every disallowed field** (`title`, `body`, `image_url`, `media_id`, `username`, `server_url`, `notification`, …) → `400 unexpected_field`.
- **Every validation failure** → its exact `error.code` (`invalid_token`, `invalid_environment`, `invalid_mode`, `invalid_collapse_id`, `topic_not_allowed`, `project_not_allowed`, `package_not_allowed`, `missing_idempotency_key`, …).
- The same `pushwire` golden corpus is shared with `silo-server`'s `custom_apns`/`custom_fcm` tests. Because a copied package in one repo cannot fail the other repo's CI on its own, the mirror is a **checked-in generated artifact with a committed checksum** that **both** repos verify in CI against the canonical hash (see §3 sync runbook) — a divergence fails CI on both sides regardless of which repo changed first.
- **Closed-allowlist assertion (privacy guarantee):** a dedicated test asserts the FCM `data` map keys and the APNs payload key set are **exactly** the closed allowlist, so adding any key (e.g. smuggling content into `data`) fails CI even if golden files are regenerated. The content-free guarantee is enforced structurally, not only by golden equality.

### 5.3 Integration against sandbox / validateOnly

- **APNs sandbox** (`api.sandbox.push.apple.com`): a real send returns 200 + `apns_id`; a deliberately invalid token returns the mapped `bad_device_token` / `unregistered`. Run behind `-tags=integration`, only when `.p8` secrets are present (CI `integration` job).
- **FCM `validateOnly:true`** (reference §2.5): a valid request validates without delivering; a malformed token surfaces `invalid_argument`/`unregistered`. **Never** `validateOnly` on production sends.

### 5.4 Redaction assertions

Capture log + op-log output across success and every error path; assert via regex that none of: the bearer value, the raw `rk_` secret, a raw device token, the relay JWT, or an OAuth2 access token ever appears, and that `token_hash` is present where a token was involved (spec §12/§13.1).

### 5.5 Load / pacing test (hundreds-of-users burst)

Simulate a burst of several hundred concurrent device sends (one account, mixed APNs/FCM) against a **fake upstream** that records call counts and timing:

- Assert per-account rate limiting holds (10 req/s burst + 50k/day; spec §9.2) and over-limit responses are `429` with a **relative** `Retry-After`.
- Assert one upstream HTTP/2 connection is reused (no connection-per-push) and sustains the burst (reference §1.3/§3.1).
- Assert latency percentiles and zero double-sends.

### 5.6 Idempotency-under-concurrency test

Fire N (e.g. 50) concurrent requests sharing one `Idempotency-Key` at a fake upstream that increments a counter per call. Assert: **exactly one** upstream call; the other N−1 either replay the stored result or receive `409 idempotency_conflict`; a follow-up with a **different** payload but the same key returns `422 idempotency_key_reuse`. This proves at-most-once upstream send under the caller's concurrent outbox retries (spec §10.2).

### 5.7 Failure-mode tests (the exact modes on-call hits)

Beyond the happy path and the error-table mappings, the plan tests every failure mode that actually pages an engineer (all use fakes — no provider creds):

- **APNs `GOAWAY` / mid-stream reset.** A fake APNs upstream injects `GOAWAY` (or resets the stream) before a final response mid-burst → the caller-facing result is `503 upstream_unavailable`, the op-log/metric is tagged `connection_severed`, and a counting fake confirms **no lost or double send** (no transparent retry).
- **JWT expiry under concurrency.** Force the cached APNs JWT to expire under a concurrent burst → assert **exactly one** single-flighted regeneration and **zero** `429 TooManyProviderTokenUpdates`.
- **Redis dropped mid-request** (not just down at start) → idempotency fails **closed** (`503`); rate-limit degrades to **bounded** fail-open; a lock whose TTL expires mid-send cannot be stolen (upstream deadline `<` `lock_ttl`).
- **Graceful-drain idempotency.** A rolling restart during in-flight sends leaves **no orphaned in-flight idempotency markers** (an immediate caller retry does not eat a spurious `409`); `/readyz` flips to `503` before draining.
- **FCM quota-exhaustion shedding.** With the shared project quota exhausted, the relay-side project circuit breaker sheds with `503 upstream_throttled` instead of forwarding into a 429 storm.

---

## 6. Deployment

### 6.1 Container build

- `deploy/Dockerfile`: multi-stage; static `relay` (and `relayctl`) binaries on a distroless/minimal base. **No credentials baked in** (spec §11). Non-root user; read-only filesystem where possible.
- Image carries only the binary + CA roots (ensure USERTrust RSA present — default in `ca-certificates`; spec §6.3).

### 6.2 Secret provisioning

| Secret | Source | Notes |
|---|---|---|
| APNs `.p8` + Key ID + Team ID | secret manager (or mounted file) | per official team; never env/image (spec §14.3) |
| FCM SA JSON **or** Workload Identity | GCP keyless (on-GCP) / secret manager JSON (off-GCP) | open Q#4 (spec §15) |
| API-key HMAC pepper | secret manager | rotation is heavier (open Q#6) |
| Postgres DSN, Redis URL | secret manager | privileged `relayctl` DSN held only by operators |

Rotation: issue a new APNs key / FCM credential, deploy, retire the old; the relay loads secrets at startup so a rolling restart picks them up (spec §14.3). Optional SIGHUP re-read.

### 6.3 TLS, health, metrics wiring

- TLS 1.2+ (prefer 1.3), HTTP/2, HSTS on `relay.silo.app`; terminate at LB or in-process (spec §14.1).
- **Public:** `/v1/apple/send`, `/v1/fcm/send`, `/healthz`. **Internal-only:** `/readyz`, `/metrics` (spec §11/§14.1).
- Liveness probe → `/healthz`; readiness probe → `/readyz`; Prometheus scrapes `/metrics` on the internal network.

### 6.4 Monitoring / alerting

Alert on (from the §13.2 catalog):

- **First `upstream_auth_error` → page ops.** Alert on the caller-facing `upstream_auth_error` reason (the relay's genuine `.p8`/SA/OAuth2 credential failure — a non-200 from `oauth2.googleapis.com/token`, or `401 UNAUTHENTICATED`/`403 PERMISSION_DENIED`/`InvalidProviderToken` on send) > 0 → **credential misconfig, page ops**: never caller-fixable. **Do NOT** key this alert on `THIRD_PARTY_AUTH_ERROR`, which on this pure-Android path indicates a per-token/registration anomaly, not a relay-credential problem (see Phase 4 reclassification) — surface it for investigation at most, never page.
- **Proactive credential-health alert.** Alert if the periodic synthetic credential check (Phase 6: a scheduled `relayctl ping-upstream` / `validateOnly` FCM send + APNs auth-exercising probe, plus the `relay_credential_healthy` gauge wired into `/readyz`) reports the loaded `.p8`/SA/Workload-Identity credential is no longer accepted upstream — Apple actively revokes leaked `.p8` keys and SA keys/bindings can be rotated out from under the relay, so this must fire **before** production sends start failing.
- Sustained `relay_rate_limit_degraded_total` increase → Redis degraded (fail-open active; in-process per-account fallback limiter engaged).
- `relay_redis_up == 0` / `relay_pg_up == 0`; any provider showing `relay_provider_healthy{provider=...} == 0` → that upstream is degraded (per-provider, not a whole-replica outage).
- Elevated `relay_requests_total{status_code="5xx"}` rate; `relay_request_duration_seconds` p99 regression.
- `relay_upstream_requests_total{upstream_reason="QUOTA_EXCEEDED"}` and **low `relay_fcm_project_quota_headroom`** → shared FCM project quota pressure: the relay-side project limiter is shedding with `503 upstream_throttled` before hammering FCM (watch Cloud Console; spec §14.5). Treat as a multi-tenant fairness event — one noisy account must not exhaust the ~600k/min project quota shared across all accounts.
- Accounts approaching/hitting the **per-account daily cap** (`relay_rate_limited_total{kind="daily"}`) → a legitimate big-release burst can hit 50k/day; surface for an operator to consider a temporary cap bump.
- Anomalous `relay_auth_failures_total` rate from an egress IP → possible key-guessing (see §6.4 anti-brute-force; the per-IP limiter consumes the resolved client IP, not a caller-supplied header). Note: the fine-grained `reason` (unknown-prefix vs hash-mismatch) is internal-only and must not be exposed to tenants.

### 6.5 Rollout

- **Staging first** (spec §14.4): separate hostname + separate accounts so client QA never touches production quotas. Validate APNs sandbox + FCM `validateOnly` end-to-end.
- **HA / rolling** (spec §14.2): multiple stateless replicas behind the LB; rolling deploy with drain (`srv.Shutdown`); a killed replica is covered by the caller's outbox retry — **relay-down delays, never loses**.
- Single region in v1, close to PG/Redis (spec §14.5).

---

## 7. Provisioning Checklist (the long pole — start day 1)

> These gate the phases that touch live providers (Phase 3 APNs, Phase 4 FCM, Phase 6 production). Run them **in parallel** with Phases 0–2, which need no credentials.

### 7.1 Apple (gates Phase 3 + Phase 6)

- [ ] Apple Developer **Program account / team** with push entitlement (capture the 10-char **Team ID**).
- [ ] Generate an **APNs Auth Key (`.p8`)** in the developer portal; capture the **Key ID** (10 chars) and download the `.p8` **once** (Apple shows it once). Store in the secret manager (spec §14.3).
- [ ] Consider Feb-2025 **scoped keys** (team-scoped or topic-specific) for least privilege (reference §1.2).
- [ ] Register per-platform **bundle topics** for the official build config and seed the account allowlist (spec §8.4): `com.continuum.app.ios`, `com.continuum.app.tvos`, `com.continuum.app.macos`.
- [ ] Standardize on the **sandbox host** `api.sandbox.push.apple.com` (reference §1.3; `api.development.push.apple.com` is an equivalent still-resolving alias — no build-time resolution needed) and confirm the APNs server TLS root (**USERTrust RSA**, present in standard pools; spec §6.3 / reference §1.7).
- [ ] Decide `apns2` vs hand-rolled client (open Q#3) before Phase 3.

### 7.2 Firebase / Google (gates Phase 4 + Phase 6)

- [ ] Firebase **project(s)** for the official build config: `continuum-prod-android` (and a staging project if desired).
- [ ] Register the Android **package** `com.continuum.app.android`; seed the FCM allowlist `(project_id, package_name)` (spec §8.4).
- [ ] Provision a **service account** with the least-privilege `firebase.messaging` scope (reference §2.2). On GCP, prefer keyless **Workload Identity / attached SA**; off-GCP, download the **service-account JSON** and store it in the secret manager — never in repo/image (reference §2.2/§4.5; open Q#4).
- [ ] Confirm the global send host `fcm.googleapis.com` (no regional endpoints; reference §2.3) and the default ~600k msgs/min project quota (reference §2.7 / spec §14.5).

### 7.3 Cross-cutting

- [ ] Secret manager chosen and wired (KMS/Vault/cloud secret manager) for `.p8`, SA JSON, pepper, DSNs (spec §11 / Phase 6).
- [ ] DNS + TLS cert for `relay.silo.app` (and the staging hostname).

---

## 8. Risks, Mitigations, and Milestone Sequencing

### 8.1 Risks and mitigations

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | **Provider provisioning slips** (Apple/Firebase approvals) — the real long pole | Med | High (blocks Phase 3/4/6) | Start §7 on day 1 in parallel; Phases 0–2 + all unit/contract tests need no creds; fakes for upstream |
| R2 | `sideshow/apns2` is unmaintained (no release since Oct 2024) | Med | Med | Hide behind `internal/apns` interface; prototype hand-rolled `x/net/http2`+`golang-jwt/jwt/v5` in Phase 3; decide per open Q#3 |
| R3 | **JWT/token mishandling** (over-frequent refresh → APNs `429 TooManyProviderTokenUpdates`; per-request mint → throttling; concurrent-403 stampede) | Med | High | Cache + mutex; ~50-min APNs refresh (never <20min), eager-on-expired; **single-flighted regeneration** so a concurrent burst of `ExpiredProviderToken` yields exactly one refresh per ~20-min window; OAuth2 `TokenSource` auto-refresh; metrics `relay_apns_jwt_refresh_total`/`relay_fcm_token_refresh_total` |
| R4 | **Double-send** under concurrent caller retries (incl. lock-TTL expiry mid-send, and stale in-flight markers after a killed/drained replica) | Med | High | Redis `SET NX` idempotency, **fail-closed**; pin the upstream call deadline **strictly shorter than `lock_ttl`** (≈10 s upstream vs 30 s lock) and refuse to send if the deadline already passed the lock TTL; release/downgrade the in-flight marker on handler context-cancellation; at-most-once test (§5.6); drain test asserting no orphaned in-flight markers (§5.7); key includes `attempt_number` |
| R5 | **Redis outage / eviction** | Low | Med | Rate-limit **fails open but bounded** (in-process per-account fallback limiter, logged + metric); idempotency **fails closed** (`503`, retryable via caller outbox); idempotency keyspace requires a **non-evicting `maxmemory-policy` (`noeviction`)** or a dedicated Redis/db so a rate-limit eviction can never silently drop an in-flight idempotency lock (which would violate at-most-once) |
| R6 | **Content/identity leak** despite design | Low | Critical | Structural: `DisallowUnknownFields`, content-free schema, data-only FCM, badge-off-by-default, redaction in middleware, op-logs hold only `token_hash` |
| R7 | **Cross-tenant push** (one account targeting another's topic/token) | Low | High | Per-account allowlist enforced after auth; namespaced Redis keys; per-account-scoped allowlist cache |
| R8 | **Stolen relay API key** (incl. abuse-amplification during a Redis outage, and per-IP-throttle bypass via forged forwarding headers) | Low | Med | Revocable keys (`relayctl key revoke`), per-account rate caps + allowlist bound spend; **bounded** fail-open (in-process per-account cap when Redis is down — never unlimited); per-IP auth-failure throttling keyed on a **trusted-proxy-resolved client IP** (caller-supplied `X-Forwarded-For`/`X-Real-IP` ignored) |
| R9 | **`pushwire` drift** between relay and `silo-server` | Med | Med | One **canonical source repo**; mirror is a checked-in generated artifact with a committed **checksum** verified in CI on **both** sides; golden-file + closed-allowlist-key tests fail CI on divergence regardless of regeneration order |
| R10 | **APNs TLS root rotation** (USERTrust) | Low | Low | Use `api.sandbox.push.apple.com` (the `api.development.*` alias is equivalent and still resolves — not a risk); rely on the standard trust store (already includes USERTrust RSA) |
| R11 | **FCM high-priority deprioritization** over 7-day window (reference §2.8) | Low | Low | Honor `03`'s `priority:high` for `private_data` (app surfaces a notification); caller-side `priority:normal` fallback is the lever (open Q#10) |
| R12 | **Op-log volume** at 50k/day/account across many accounts (unbounded table → disk fills) | Med | Low | **Shipped** retention GC (`relayctl logs --gc` / background sweeper: 14d success / 90d failure) on a schedule with an owner; time-partitioning DDL (or a documented partition/offload decision); table-growth/disk alert |

### 8.2 Milestone sequencing & rough effort

| Milestone | Phases | Effort | Provider creds needed | Parallelizable |
|---|---|---|---|---|
| **M0 — Skeleton green** | Phase 0 | ~M (1 wk) | No | — |
| **M1 — Storage + admin** | Phase 1 | ~L (1 wk) | No | with §7 provisioning |
| **M2 — Request path (no upstream)** | Phase 2 | ~L (1 wk) | No | with §7 provisioning |
| **M3 — APNs live (sandbox)** | Phase 3 | ~L (1 wk) | **Apple `.p8`** | Phase 3 ∥ Phase 4 |
| **M4 — FCM live (validateOnly)** | Phase 4 | ~L (1 wk) | **Firebase SA** | Phase 4 ∥ Phase 3 |
| **M5 — Observability** | Phase 5 | ~M (3 d) | No | — |
| **M6 — Hardened + deployed** | Phase 6 | ~L (1 wk) | All + secret manager | — |

**Rough total:** ~6–7 engineer-weeks of code, with **§7 provisioning running in parallel from day 1** as the gating long pole for M3/M4/M6. Phases 3 and 4 can be split across two engineers once Phase 2 lands. The critical path is: M0 → M1 → M2 → (M3 ∥ M4) → M5 → M6, intersected with provisioning availability.

---

## 9. Open Questions (carried from spec §15)

These do not block starting; resolve at the noted phase.

1. **Per-account rate limits — real numbers** (resolve from logs post-M6; spec §9.2). v1 ships ~10 req/s burst + 50k/day.
2. **APNs sandbox hostname** (resolved — note only). Use `api.sandbox.push.apple.com`; `api.development.push.apple.com` is an equivalent, still-resolving sandbox alias. The two are interchangeable, so no build-time resolution is required — this is a standardization note, not an open question.
3. **`apns2` vs hand-rolled APNs client** — decide before Phase 3 (§3, reference §3.1).
4. **FCM credentials: GCP-keyless vs off-GCP JSON** — resolve in deployment design (Phase 6).
5. **`apns-expiration` / `android.ttl` defaults** (largely resolved). v1 ships a **finite** default (not omitted/30-day): a few hours for `private_alert`, short TTL for `background_wake`, with a matching finite FCM `android.ttl`; both configurable. Only the exact numeric values want product input.
6. **API-key hashing: HMAC pepper vs plain salted SHA-256** — plan chooses HMAC pepper; confirm rotation tradeoff (Phase 1).
7. **Idempotency-Key spec status** — header name + Stripe-style semantics are de-facto; re-check for an RFC.
8. **Op-log volume & retention** (resolved into a Phase 5 deliverable). Ship `relayctl logs --gc` (or a background sweeper): 14d success / 90d failure, on a schedule with an owner, plus time-partitioning DDL (or a documented partition/offload decision) and a table-growth alert — not deferred.
9. **Multi-region** — out of scope for v1; design later if latency to distant self-hosters matters.
10. **FCM priority deprioritization** — honor `03`'s `priority:high`; revisit only if metrics show systematic deprioritization.
