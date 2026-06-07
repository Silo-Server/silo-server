# Requests Fulfillment as a Pluggable Backend Category — Design Spec

**Date:** 2026-06-07
**Status:** Approved design, pre-implementation
**Goal:** Keep Silo's request system (lifecycle, quota, policy, UX) intact while turning the **fulfillment backend** — how an approved request is actually sent to and tracked against a downstream system — into an out-of-process plugin category, with multi-instance Sonarr/Radarr as the first plugin and Seerr as a planned follow-on.

**Architecture (one sentence):** Silo's host keeps the entire request lifecycle and governance in-process, and delegates *whole-request fulfillment* (routing across instances + qualities, submission, status checks) to an installable plugin through the already-declared, additive `request_router.v1` capability.

**Tech stack:** Go (silo-server host + plugins), `silo-plugin-sdk` (protobuf capability contract, consumed as a tagged module), existing `internal/plugins` + `internal/pluginhost` runtime, existing requests subsystem (`internal/requests`), React/TS admin + request UI.

Commands assume the repository root is the cwd.

---

## 1. Background & motivation

The current requests implementation is in-process Go. The host owns the full request lifecycle (create → approve → queued → downloading → completed/failed), per-user quota and approval policy, multi-instance routing, and per-quality target tracking. Fulfillment against Sonarr/Radarr is abstracted behind six Go interfaces (`MovieFulfillmentAdapter`, `SeriesFulfillmentAdapter`, `MovieStatusAdapter`, `SeriesStatusAdapter`, `MovieIntegrationOptionsAdapter`, `SeriesIntegrationOptionsAdapter`), wired in `cmd/silo/main.go` via `SetFulfillmentAdapters(radarr.NewClient(...), sonarr.NewClient(...))`. The concrete arr code lives in `internal/requests/radarr/`, `internal/requests/sonarr/`, `internal/requests/arrclient/`, and the instance-routing half of `internal/requests/routing.go`.

Two structural problems:

1. **It is arr-only and not extensible.** Supporting Seerr — or any other request backend a third party might write — means more bespoke host code. The request-backend concept should be agnostic.
2. **The backend mechanics are baked into the host.** Sonarr/Radarr HTTP behavior, quality-profile/root-folder vocabulary, and queue-evaluation semantics all live in the host binary.

Silo already ships a full out-of-process plugin runtime (`internal/plugins`, `internal/pluginhost`): manifests, an installer, typed capability descriptors (`metadata_provider.v1`, `scan_source.v1`, `request_router.v1`, `media_analyzer.v1`, `scheduled_task.v1`, `event_consumer.v1`, …), a host-driven scheduled-task mechanism, and a rate-limited plugin→host event path. Crucially, **`request_router.v1` is already declared as a known capability constant (`pkg/pluginsdk/capability/capability.go`) but has no proto** — a half-anticipated slot this design fills. The autoscan pluginization (`2026-06-02-autoscan-plugin-architecture-design.md`) established the exact pattern this design follows: a stateless plugin that receives host-owned connection credentials per call.

## 2. Goals / non-goals

**Goals**

- A backend-agnostic `request_router.v1` plugin capability — **breaks no existing plugin**; agnostic enough that third parties can add request backends with **no host changes**.
- The request system's lifecycle, quota, policy, UX, and API contract stay behaviorally unchanged.
- Multi-instance Sonarr/Radarr re-implemented as the first `request_router` plugin (`silo-plugin-requests-arr`); the host's arr logic relocates into it.
- **Pure-plugin** fulfillment (no in-host fallback), matching autoscan: with no plugin installed, requests can be created but not submitted.
- Preserve autoscan's reuse of the existing Sonarr/Radarr connection rows (`request_integration_id` soft-link).

**Non-goals (this design)**

- `silo-plugin-requests-seerr` — purely additive on the finished contract; its own spec/plan cycle.
- Refactoring the request lifecycle, quota, or policy engines.
- Android/Apple client changes — the public request API (lifecycle/status/targets) is unchanged; this spec flags it for confirmation only.

## 3. Architecture overview

```
┌──────────────────────────── Silo (host) ────────────────────────────┐
│  Requests subsystem (lifecycle/governance — UNCHANGED behavior)      │
│    • create / approve / decline / cancel / retry                     │
│    • quota, EffectivePolicy, per-user limits                         │
│    • allowed_qualities computation (entitlement ceiling + settings)  │
│    • media_request_targets records + aggregate status                │
│    • reconcile task scheduling, events audit trail                   │
│                              │                                       │
│  Connection registry (generic, two-tier)                            │
│    request_integrations: id, name, enabled, is_default,             │
│      base_url, api_key_ref, capability_id, installation_id,         │
│      supported_media_types, plugin_config JSONB                      │
│                              │                                       │
│                              ▼                                       │
│  RequestRouterProvider — plugin-backed seam (replaces 6 adapters)    │
│    on approve:  Fulfill(descriptor, allowed_qualities, connections) │
│    on reconcile: CheckStatus(descriptor, targets, connections)       │
│    admin form:  ListConfigOptions(connection) / TestConnection(...)  │
└───────────────────────────────┬──────────────────────────────────────┘
                                 │ gRPC over go-plugin (per-call creds)
                                 ▼
        ┌──────────────── silo-plugin-requests-arr ────────────────┐
        │  request_router.v1 (stateless)                            │
        │    Fulfill: run routeTargets internally, submit to        │
        │      Radarr/Sonarr instances, return one target/quality   │
        │    CheckStatus: re-query arr queues per target            │
        │    ListConfigOptions: live root folders/profiles/tags     │
        │    TestConnection: reachability + key check               │
        │  internal/arr/: radarr + sonarr + arrclient + routing     │
        │    (relocated from host, tests intact)                    │
        └───────────────────────────────────────────────────────────┘
```

The host computes the **governance constraint** (`allowed_qualities`) and hands the request plus the eligible connections to the plugin. The plugin owns **routing** (which instances, which qualities map to which instances, anime/4k overlay) and **submission**, then reports back the targets it actually created. The host persists those targets and reconciles them. The contract carries **no arr vocabulary** — root folders, quality profiles, and `service_kind` (radarr vs sonarr) live inside the plugin's declared config schema and the opaque `plugin_config` blob.

## 4. The `request_router.v1` SDK contract

New `proto/silo/plugin/v1/request_router.proto` in `silo-plugin-sdk`, following the `scan_source` precedent (stateless plugin; host passes `ResolvedConnection` per call).

```protobuf
service RequestRouter {
  // Submit a whole approved request; plugin routes across instances+qualities.
  rpc Fulfill(FulfillRequest) returns (FulfillResponse);
  // Re-check status of previously-created targets (host reconcile loop).
  rpc CheckStatus(CheckStatusRequest) returns (CheckStatusResponse);
  // Live option data for the admin connection form (e.g. root folders, profiles, tags).
  rpc ListConfigOptions(ListConfigOptionsRequest) returns (ListConfigOptionsResponse);
  // Validate a connection's reachability/credentials (admin "Test" + health).
  rpc TestConnection(TestConnectionRequest) returns (TestConnectionResponse);
}
```

**Core messages (agnostic — no arr terms):**

- `RequestDescriptor`: `media_type` (`movie`|`series`), `title`, `year`, `external_ids` (generic `map<string,string>`: tmdb/tvdb/imdb…), `is_anime`, requester context (user/profile id for backends that tag by requester).
- `QualityConstraint`: host-computed `allowed_qualities` (repeated quality enum/string). This is the entitlement-ceiling governance output; the plugin must not exceed it.
- `ResolvedConnection`: `id`, `base_url`, `api_key`, and `plugin_config` (`google.protobuf.Struct`) — the two-tier blob the plugin declared the schema for. Resolved (decrypted) by the host before the call; never stored by the plugin.
- `FulfillmentTarget` (plugin → host): `quality`, `connection_id`, `external_id`, `external_status`, `status`, optional `message`. The host maps these onto `media_request_targets` rows.
- `ListConfigOptionsRequest/Response`: `connection`, optional `field` key → list of `{value, label}` options (powers dynamic admin dropdowns).
- `TestConnectionResponse`: `ok`, `message`, optional discovered metadata.

**SDK plumbing (mirrors the marker_provider addition, commit `c832d91` precedent):**

1. `proto/silo/plugin/v1/request_router.proto` — new service + messages.
2. `pkg/pluginsdk/capability/capability.go` — `RequestRouter` constant already present; ensure it is in `KnownTypes`.
3. `pkg/pluginsdk/runtime/runtime.go` — add `RequestRouter` to `CapabilityServers`, a `RequestRouter()` client accessor, and server registration in `GRPCServer()`.
4. `pkg/pluginsdk/manifest/` — manifest-load test accepting a `request_router.v1` capability.
5. `make proto` regenerates stubs under `pkg/pluginproto/silo/plugin/v1/`.

The SDK change is **purely additive** — no existing capability, message, or plugin is touched.

## 5. Host refactor

### 5.1 Connection registry (two-tier, in place)

Generalize the existing `request_integrations` table rather than introduce a new one, so autoscan's `request_integration_id` FK and `RequestIntegrationLookup` adapter keep resolving against unchanged rows.

- **Keep** generic columns: `id`, `name`, `enabled`, `is_default`, `base_url`, `api_key_ref`.
- **Add**: `capability_id` (which plugin capability owns this connection), `installation_id` (which plugin install), `supported_media_types` (generic host-visible hint, e.g. `{movie}` / `{series}`), `plugin_config JSONB` (the opaque blob conforming to the plugin's declared schema).
- **Migrate** arr-specific columns (`root_folder`, `quality_profile_id`, `tags`, `is_4k`, `anime_*`, `kind`) into `plugin_config` (with `kind`→`service_kind`). A Goose migration (timestamped, via `make migrate-create`) copies existing data into the blob and sets `capability_id = request_router.v1`, `supported_media_types` derived from `kind`.

### 5.2 Seam swap

- **Delete** the six fulfillment Go interfaces and the host implementations: `internal/requests/radarr/`, `internal/requests/sonarr/`, `internal/requests/arrclient/`, and the instance-routing logic in `internal/requests/routing.go` (`routeTargets`, `resolveInstance`).
- **Add** a single `RequestRouterProvider` backed by `pluginhost`, structurally identical to autoscan's `ScanSourceProvider`/`pluginProvider`. `cmd/silo/main.go`'s `SetFulfillmentAdapters(...)` becomes provider wiring through the plugin host.
- `internal/requests/service.go`'s `submitApprovedRequest` **inverts**: instead of computing targets via `routeTargets` then calling arr clients, it computes only the `allowed_qualities` constraint, gathers enabled `request_router` connections matching the request's `media_type`, calls `provider.Fulfill(...)`, and **persists whatever targets come back**. `reconcileRequest`/`checkFulfillmentStatus` call `provider.CheckStatus(...)`.

### 5.3 What stays host-side (unchanged behavior)

Request lifecycle (create/approve/decline/cancel/retry), quota + `EffectivePolicy` + per-user limits, the `allowed_qualities` computation (entitlement ceiling + `force_dual_quality`), `media_request_targets` records + aggregate status recomputation, the reconcile task and its 5-minute schedule, the `media_request_events` audit trail, and all React request pages/hooks.

### 5.4 Admin integration form → plugin-driven

The admin "integrations" form stops hard-coding arr fields. It renders the plugin capability's declared `config_schema`/`admin_form` (same machinery tmdb uses), fills live dropdowns via `ListConfigOptions`, and wires the "Test" button to `TestConnection`. With no `request_router` plugin installed, the form shows a "no fulfillment backend installed" empty state.

## 6. `silo-plugin-requests-arr` (new repo)

Mirrors `silo-plugin-autoscan-arr`:

```
silo-plugin-requests-arr/
├── main.go                    # Runtime + RequestRouter servers; embeds + checksums manifest
├── manifest.json              # declares request_router.v1 capability + per-connection config schema
├── internal/arr/              # relocated from host, tests intact
│   ├── radarr.go              # SubmitMovie / status / options   (was internal/requests/radarr)
│   ├── sonarr.go              # SubmitSeries / status / options  (was internal/requests/sonarr)
│   ├── client.go              # generic arr HTTP client          (was internal/requests/arrclient)
│   ├── routing.go             # routeTargets / resolveInstance    (was internal/requests/routing.go)
│   └── *_test.go
├── Makefile
└── .github/workflows/{ci,release}.yml
```

- The manifest declares the `request_router.v1` capability and the **per-connection config schema**: `service_kind` (`radarr`|`sonarr`), `root_folder`, `quality_profile_id`, `tags`, `anime_enabled`, `anime_root_folder`, `anime_quality_profile_id`, `anime_tags`, `is_4k` — each with an `admin_form` descriptor. `supported_media_types` is derived (`radarr→movie`, `sonarr→series`).
- `Fulfill` runs the relocated `routeTargets` logic *within the `allowed_qualities` constraint*, submits to each instance, and returns one `FulfillmentTarget` per quality it submitted.
- `CheckStatus` re-queries arr queue details per target and maps to `status`/`external_status`/`message`.
- The plugin is **stateless** — connection config arrives in every call as `ResolvedConnection.plugin_config`; it persists nothing.
- Build/release reuse the autoscan-arr pattern: cross-compile, checksum the manifest, dispatch to the `silo-plugins` catalog.

## 7. Autoscan reuse — preservation & verification

Autoscan soft-links to Sonarr/Radarr connection rows via `request_integration_id` and resolves credentials through `RequestIntegrationLookup`, which reads only the generic columns (`base_url`, `api_key_ref`). Those columns and the row ids are unchanged by §5.1; only arr-specific columns relocate into `plugin_config`, which autoscan never read. The link therefore survives untouched.

**Explicit regression check (required in the plan):** an autoscan source linked via `request_integration_id` still resolves credentials and polls successfully after the migration.

## 8. Data flow

**Fulfill (on approve):**
1. Host computes `allowed_qualities` from entitlement ceiling + settings (`force_dual_quality`).
2. Host gathers enabled `request_router` connections whose `supported_media_types` includes the request's `media_type`.
3. Host calls `provider.Fulfill(descriptor, allowed_qualities, connections)`.
4. Plugin routes/submits, returns `[]FulfillmentTarget`.
5. Host writes `media_request_targets`, recomputes aggregate status, records an event.

**Reconcile (every 5 min, schedule unchanged):**
1. Host loads candidates + their targets.
2. Host calls `provider.CheckStatus(descriptor, targets, connections)`.
3. Plugin re-queries downstream state, returns per-target status.
4. Host updates targets + aggregate + events.

**Admin config:** form renders plugin `config_schema`; dropdowns populate via `ListConfigOptions`; "Test" calls `TestConnection`.

## 9. Errors & edge cases

- **No plugin installed:** requests can be created but **approve is blocked with an explanatory error** ("no fulfillment backend configured"). (Chosen over silent queueing for honesty/simplicity; a future spec may add a pending-backend queue.)
- **Partial fulfillment:** plugin returns targets for the qualities it succeeded on, plus per-target `message` for failures; host persists partial success (matches today's per-target failure model).
- **Quality unsupported by a backend:** plugin omits that target with a `message`; host shows it as failed-for-quality.
- **Plugin crash/timeout:** `pluginhost` health loop + per-call deadlines (same as autoscan); the request stays `approved` and is retried on the next reconcile tick.
- **Connection points at an uninstalled/disabled plugin:** treated as no eligible backend for that media type; surfaced in admin + approve path.

## 10. Testing

- **SDK:** manifest-load test accepting `request_router.v1`; generated stubs compile; `make proto` is clean.
- **Host:** `RequestRouterProvider` tested against a fake plugin client; migration test (arr columns → `plugin_config`, row ids preserved); **autoscan-reuse regression test** (§7).
- **Plugin:** the relocated arr unit tests run green in-repo; a `Fulfill`/`CheckStatus` round-trip against a mock arr HTTP server.
- **Environment note:** host handler tests that touch libvips/CGO must run in the `golang:1.26+libvips-dev` container — a bare `go test ./...` silently skips those packages.

## 11. Sequencing (high-level; full plan via writing-plans)

1. SDK: add `request_router.proto`, runtime wiring, manifest test, regenerate, tag a module version.
2. Host: migration (two-tier connection registry); `RequestRouterProvider`; invert `submitApprovedRequest`/reconcile; delete in-host arr code; plugin-driven admin form; "no backend" UX.
3. Plugin: `silo-plugin-requests-arr` — relocate arr code, manifest, build/release, publish to catalog.
4. Verify end-to-end (fulfill + reconcile + autoscan reuse) against a live arr; confirm no client-repo follow-up needed.

## 12. Out of scope / follow-on

- `silo-plugin-requests-seerr` (separate spec/plan) — proves the contract is truly agnostic.
- Android/Apple clients — request API contract unchanged; flagged for confirmation only.
- A pending-backend approval queue (only if product wants approve-without-installed-plugin later).
