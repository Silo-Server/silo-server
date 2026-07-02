# OPA Policy Engine — Design Spec

**Date:** 2026-07-02
**Branch context:** `main` in `silo-server`
**Status:** Approved — ready for implementation plan
**Related plan:** [`../plans/2026-07-02-opa-policy-engine.md`](../plans/2026-07-02-opa-policy-engine.md)

## Goal

Embed Open Policy Agent (OPA) as Silo's authorization engine. All
request-level access decisions — viewer scope resolution, admin/permission
gates, and download/playback action checks — evaluate through one embedded
policy engine running vendor-authored Rego, with a first-class admin policy
editor for household-specific custom rules.

User-visible outcome:

- Server admins get a new **Policy** admin page: view the built-in (vendor)
  Rego, author custom override policies with inline compile validation,
  simulate decisions against sample inputs before activating, browse an
  immutable version history with one-click rollback, and query a structured
  decision audit log ("who was allowed/denied what, when, and why").
- Custom policies unlock rules the current fixed schema cannot express:
  time-of-day / schedule-based parental controls, per-collection or
  per-library exceptions, device-based restrictions — all *narrowing-only*
  (a custom policy can tighten access, never widen it).
- Non-admin users see no change. The existing profile/parental-control
  behavior is reproduced exactly by the vendor policy.

## Why OPA (research summary)

- **Embedding mode:** pure-Go library `github.com/open-policy-agent/opa/v1/rego`
  (Apache-2.0, CNCF-graduated, monthly release cadence). No sidecar, no WASM.
  Measured locally against OPA v1.18.1: **~+19 MB binary**, **~14 µs per
  prepared-query evaluation** for an RBAC/ABAC-shaped policy — negligible at
  household scale, acceptable binary cost.
- **Rego v1** (OPA ≥ 1.0, Dec 2024): `if`/`contains` mandatory, strict mode
  default, import path `github.com/open-policy-agent/opa/v1/...`. Greenfield
  adoption means no v0 compatibility machinery.
- **Ecosystem precedent** for self-hosted single-binary apps (Chef Automate,
  Pomerium, Flipt): vendor-authored, unit-tested Rego + admin extension is the
  winning pattern; sidecar deployments are the pattern to avoid.
- **Security posture:** the one serious recent CVE (CVE-2025-46569) affects
  only the OPA *server REST API*, which we do not run. Untrusted admin Rego is
  sandboxed via `ast.Capabilities` lockdown (CVE-2022-36085 showed
  `WithUnsafeBuiltins` alone is bypassable — capabilities is the supported
  mechanism) plus per-eval context timeouts.
- **Deferred but designed-for:** since OPA v1.9.0 the Compile API
  (`v1/rego/compile`) turns policies in the "filtering fragment" into SQL
  WHERE clauses (postgresql dialect). This could eventually replace the
  hand-built `catalog.AccessFilter` predicates; the PDP seam in this design is
  where that lands later.

## Hard constraints

- **`access.Scope` contract is frozen.** The resolved scope struct
  (`internal/access/types.go`) — `AllowedLibraryIDs` (nil = unrestricted),
  `DisabledLibraryIDs`, `LibrariesRestricted`, `MaxContentRating`,
  `MaxPlaybackQuality`, `PreferredMetadataLanguage`, `PolicyRevision`,
  `ProfileVerified` — keeps its exact shape and semantics. OPA is a drop-in
  behind the existing `middleware.ViewerResolver` interface.
- **Crypto stays in Go.** PIN verification and profile-token validation
  (`internal/access/profile_token.go`) are never re-implemented in Rego; the
  policy receives `profile_verified` as an input fact.
- **`access_policy_revision` semantics preserved:** bumps invalidate profile
  tokens; `users.library_ids` changes deliberately do NOT bump it. The new
  global policy generation counter is a *separate* concept and never bumps
  per-account revisions.
- **Fail closed.** Any eval error, timeout, missing result, or malformed
  decision document is treated as the most-restrictive outcome for that
  surface. No adapter ever falls back to "allow" on engine error.
- **`/api/v1` additive-only** + capability endpoint for feature detection
  (pattern: `internal/api/handlers/downloads.go` `HandleCapability`).
- **Performance first.** Prepared queries only; input-document-only data
  strategy (no per-request DB reads inside Rego, no catalog mirroring into
  OPA's store); decision logging is async and never adds eval-path latency.
- **Narrowing-only custom policy.** Admin overrides can tighten the vendor
  baseline decision but cannot widen access, by construction (see Layering).
- **Direct replacement rollout.** No shadow mode, no legacy/OPA runtime
  toggle. Parity is proven by tests before each surface cuts over; cutover is
  staged per-surface across PRs; legacy Go logic is deleted only after a
  release of bake time.
- Migrations are timestamped Goose files via `make migrate-create`; the
  decision-log table uses `internal/partman` partition management like
  `operational_logs` / `activity_log`.

## Scope

### In (v1)

1. **Viewer scope resolution** — `internal/access/resolver.go` logic ported
   to vendor Rego, evaluated behind the existing `ViewerResolver` interface.
   Covers the native API, jellycompat, ABS compat, notifications, and the
   reconciler (all five `access.NewResolver` construction sites).
2. **Route/permission gates** — acting-admin (`RequireActingAdmin`) and the
   `marker_edit` / `metadata_curation` permission checks
   (`internal/auth/permissions.go`, `internal/api/middleware/permissions.go`).
3. **Download/playback action decisions** — download allowed, transcode
   allowed, quality preset gating (`internal/downloads/policy.go`), and
   playback admission limits (`playback.SessionLimitProvider`). Go keeps
   ownership of live session *counting*; the policy decides given the counts.
4. **Admin policy editor** — dedicated `/admin/policy` page: vendor viewer,
   CodeMirror-based custom policy editor with server-side compile validation,
   stateless simulate runner, immutable version history with activate/rollback,
   decision-log browser.
5. **Decision audit logging** — partitioned `policy_decisions` table with
   sampling/verbosity controls and retention cleanup.

### Out (deferred, must not be precluded)

- **Compile-to-SQL list filtering** — `catalog.AccessFilter` SQL predicates
  stay hand-written Go. The `PDP`/`DecisionName` seam is where a
  `CompileToSQL` method lands later.
- **Plugin route/RPC gating** — plugins keep their flat
  public/authenticated/admin route model.
- **Widening ("grant") overrides** — e.g. "this device may exceed the profile
  quality cap". Requires a separately gated elevated-override path; v1 is
  narrowing-only.
- **Persisted policy test suites** — the simulate runner is stateless in v1;
  a `policy_test_cases` table is a clean additive follow-up.
- **Mobile/TV client surfaces** — v1 is server-side enforcement plus the web
  admin UI only. Clients need no changes (capability endpoint exists for
  future client awareness).

## Architecture overview

One new Go package, `internal/policy`, owns everything OPA. Nothing outside
it imports `github.com/open-policy-agent/opa/v1/*`.

```
internal/policy/
  engine.go            // compiled bundle + prepared queries, atomic swap, Evaluate
  pdp.go               // typed decision methods over Engine (one per surface)
  input.go             // input/output document structs (the policy-author contract)
  store.go             // Postgres CRUD: documents, versions, active pointer, generation
  compile.go           // CompileCheck (capabilities lockdown, package-path enforcement)
  simulate.go          // stateless test runner (throwaway bundle, never the live engine)
  decisionlog.go       // async sampled writer -> policy_decisions
  decisionlog_repo.go  // cursor-paginated query API for the admin log viewer
  system.go            // System lifecycle (mirrors notifications.System): Start, reload,
                       //   EventBus subscribe, poll fallback
  vendor.go            // go:embed vendor bundle assembly
  errors.go
  vendor/              // embedded Rego source
    scope.rego  scope_test.rego
    permission.rego  permission_test.rego
    action.rego  action_test.rego        // downloads + playback actions
    lib/ratings.rego  lib/quality.rego   // rank tables ported from internal/access
```

Thin adapters live where they are consumed (matching how
`jellycompat.NewScopeAccessFilter` wraps the resolver today):

- `internal/access/policy_resolver.go` — implements `middleware.ViewerResolver`.
- `internal/api/middleware/policy_gates.go` — acting-admin + permission
  middleware backed by the PDP.
- `internal/downloads` / playback limit provider — call the PDP's action
  decision.

### PDP: typed decision methods

Three typed methods on a `PDP` struct (not a generic `map[string]any`
`Decide`): `ResolveViewerScope(ctx, ScopeInput) (ScopeDecision, Meta, error)`,
`CheckPermission(ctx, PermissionInput) (PermissionDecision, Meta, error)`,
`CheckAction(ctx, ActionInput) (ActionDecision, Meta, error)`. Each builds
the input document, evaluates the corresponding prepared query
(`data.silo.scope.decision`, `data.silo.permission.decision`,
`data.silo.action.decision`) under a `context.WithTimeout` (default 25 ms,
setting `policy.eval_timeout_ms`), fires an async decision-log write, and
returns a typed result. Callers translate errors into their surface's
fail-closed outcome.

A future surface = one new Rego package + one input struct + one PDP method.

### Vendor vs custom layering — the override contract

- Vendor packages (`data.silo.scope`, `data.silo.permission`,
  `data.silo.action`) ship via `go:embed`, are never DB rows, never editable,
  and upgrade atomically with the binary.
- Admin documents must declare `package silo_custom.<domain>` (enforced at
  compile-check by inspecting the parsed module's package path; mismatch is a
  422).
- Each vendor terminal rule computes `base_decision` (exactly reproducing
  today's Go logic), then applies the extension point:

  ```rego
  decision := custom if {
      custom := data.silo_custom.scope.override(base_decision, input)
  } else := base_decision
  ```

  Admins implement `override(base, input)` against a documented, versioned
  contract. An absent/undefined custom document is a no-op by construction.
- **Narrowing is enforced in the vendor Rego itself**, not by heuristics: the
  vendor rule merges the override result by intersecting library sets, taking
  the minimum of quality/rating ceilings, AND-ing boolean allows — i.e. the
  vendor consults the override *only in the tightening direction*. A custom
  policy that returns a wider decision simply has no effect. This is cheap,
  runs at eval time, and cannot be bypassed by rule-shadowing because the
  custom namespace is disjoint from the queried vendor namespace.
- Input documents carry a `schema_version` field plus request-context fields
  the vendor policy ignores but overrides may use (`request_time` RFC3339,
  `device_id`, `client_ip`, `is_api_key`) — this is what enables schedule and
  device rules without vendor changes.

### Engine lifecycle

- **Compile:** vendor modules (full capabilities) + all enabled custom
  documents' active versions (locked capabilities) compile into one bundle;
  three prepared queries are built from it. A custom document that fails
  compile at reload time is skipped with a WARN and excluded — one bad row
  never takes the engine down (save-time compile-check makes this path
  near-unreachable).
- **Atomic swap:** prepared queries + revision swap under a single RWMutex
  pointer replace; readers never see a half-compiled bundle.
- **Revision:** a single-row `policy_generation` table holds a monotonic
  counter, bumped transactionally with every activate/rollback/enable/disable.
  Decision-log rows record the generation that produced them.
- **Cross-node invalidation:** a new `cache.EventPolicyChanged` constant
  published on the existing `cache.ChannelAdmin` after every activation; every
  node's `policy.System` subscriber reloads from the store. A 60 s poll
  fallback compares `policy_generation.generation` to the engine's loaded
  generation (mirrors `nodeconfig.Watcher`) for Redis-less deployments.
- **Fatal vs degraded:** vendor bundle failing to compile is startup-fatal
  (it ships with the binary; this is a build defect). A reload failure at
  runtime keeps serving the last known-good bundle and logs an error.

### Sandboxing untrusted admin Rego

1. `ast.Capabilities` lockdown: strip `http.send`, all `net.*`,
   `opa.runtime`, `rego.parse_module` and any other non-pure builtins from
   `ast.CapabilitiesForThisVersion()`; applied to custom-module compilation at
   save, activate, and reload (defense in depth).
2. Compile-check before persist: `POST .../versions` compiles the candidate
   layered over vendor + other active documents; failures return structured
   `{row, col, message}` errors and nothing is persisted as activatable.
3. Eval timeout: 25 ms default per decision (measured eval is ~14 µs; the
   timeout is a circuit breaker for pathological comprehensions).
4. Editor reachable only through `requireActingAdmin`.

## Input/output document contracts

Defined as JSON-tagged Go structs in `internal/policy/input.go`; rendered in
the editor's reference panel. All inputs carry `schema_version: 1`. Breaking
changes to these documents follow the same additive-only discipline as
`/api/v1`.

**ScopeInput** (per authenticated request): user facts
(`account_library_ids` nil=unrestricted, `account_restricted`,
`account_max_playback_quality`, `access_policy_revision`,
`disabled_library_ids`), profile facts (`profile_present`, rating/quality
ceilings, `profile_library_restricted`, `profile_allowed_library_ids`,
`profile_has_pin`, `profile_verified` — precomputed in Go), request context
(`session_id`, `request_time`, `device_id`, `client_ip`, `is_api_key`).
**ScopeDecision** mirrors `access.Scope`'s policy-derived fields plus an
explicit `unrestricted` boolean — the adapter maps `unrestricted: true` to Go
`nil` `AllowedLibraryIDs`, otherwise the (possibly empty) slice. This is the
single most dangerous JSON round-trip (Rego has no nil-vs-empty distinction)
and gets dedicated combinatorial tests.

**PermissionInput:** `user_id`, `role`, `enabled`, `assigned_permissions`,
`permission` (`acting_admin` | `marker_edit` | `metadata_curation`),
`acting_as_primary` (profile-primary DB lookup stays in Go),
`target_library_ids` / `user_library_ids` for item-scoped checks.
**PermissionDecision:** `{allowed, reason}`.

**ActionInput:** `action` (`download` | `download_transcode` | `stream` |
`transcode`), user flags (`download_allowed`, `download_transcode_allowed`,
`max_streams`, `max_transcodes`), live counts (`current_active_streams`,
`current_active_transcodes` — computed by Go exactly as today), quality/rating
context (`requested_quality`, `file_quality`, `max_playback_quality`,
`content_rating`, `max_content_rating`), `library_id`, `request_time`,
`device_id`. **ActionDecision:** `{allowed, reason, quality_ceiling}` (the
ceiling may tighten further than Scope).

## Data model

Four tables in one migration (`make migrate-create NAME=policy_foundation`):

- **`policy_documents`** — `id`, `domain` (`scope`|`permission`|`action`),
  `name`, `enabled`, `active_version_id` (FK, nullable), timestamps.
  UNIQUE(domain, name).
- **`policy_document_versions`** — immutable: `document_id`, `version_number`
  (per-document, UNIQUE together), `rego_source`, `source_sha256`,
  `compiled_ok`, `compile_error`, `created_by_user_id`, `comment`,
  `created_at`. Every save inserts a new row; rollback flips
  `active_version_id`. The versions table is the authoring audit trail.
- **`policy_generation`** — single-row monotonic counter (see Lifecycle).
- **`policy_decisions`** — decision log, `PARTITION BY RANGE ("timestamp")`
  with a DEFAULT partition, managed by `partman.NewManager(pool,
  "policy_decisions", partman.Daily, 3)`. Columns: `decision_name`,
  `policy_generation`, `user_id`, `profile_id`, `session_id`, `request_id`,
  `node_id`, `allowed` (NULL for scope), `eval_time_ns`, `input_digest`
  (sha256 of canonical input JSON), `input_sample` / `result_sample` JSONB
  (verbosity-gated), `error`. Indexes on (timestamp DESC, id DESC),
  (decision_name, timestamp DESC), (user_id, timestamp DESC), and a partial
  index WHERE allowed = false.

Custom Rego source is **not** a secret — plaintext rows, no
`SensitiveSettingKeys` entry (consistent with `theme` custom CSS).

## Decision logging

- Async writer: buffered channel + batch-insert flush goroutine (shape of
  `activitylog`'s writer). A full buffer drops-and-counts; logging failure
  never blocks or fails a decision.
- **Sampling/verbosity** (hot-reloaded `server_settings` keys):
  - `policy.decision_log_verbosity`: `digest` (default; no payloads) |
    `verbose` (adds input/result JSONB, gated by sample rate).
  - `policy.decision_log_scope_sample_rate`: scope decisions run on every
    authenticated request; default 1-in-50 sampling. Permission/action
    decisions log at 100 %.
  - **Denials and eval errors always log**, at full fidelity when verbosity is
    `verbose` — denials are the audit trail's whole point.
- Retention: `policy.decision_log_retention_days` (default 14) enforced by a
  `taskmanager` cleanup task dropping expired partitions, registered beside
  `ActivityLogCleanupTask` / `OperationalLogCleanupTask`.

## API design (all additive)

`GET /api/v1/policy/capability` — authenticated, not admin-gated:
`{enabled, editor_available, decision_types, generation}`.

Under `/api/v1/admin/policy/*` (acting-admin gated):

| Endpoint | Purpose |
|---|---|
| `GET /vendor` | read-only embedded vendor Rego, per module |
| `GET /documents` / `POST /documents` | list / create (empty) document |
| `GET /documents/{id}` | document + active version source |
| `GET /documents/{id}/versions` (+ `/{version}`) | version history / single source |
| `POST /documents/{id}/versions` | compile-check + persist new version (NOT activated); 422 with `{errors:[{row,col,message}]}` on failure |
| `POST /documents/{id}/versions/{version}/activate` | flip pointer, bump generation, publish invalidation (rollback = activate an older version) |
| `POST /documents/{id}/enabled` | kill switch without deleting history |
| `DELETE /documents/{id}` | only when no version is active |
| `POST /validate` | stateless compile-check (editor's Validate button) |
| `POST /simulate` | `{domain, source?, decision_name, input}` → decision + `eval_time_ns` + trace, against a throwaway bundle; no decision-log row |
| `GET /decisions` (+ `/{id}`) | cursor-paginated decision log query (`decision_name`, `user_id`, `allowed`, time range) |

## Frontend

Dedicated admin page (`/admin/policy`, new entry in
`web/src/lib/adminNavigation.ts` under System) — not a settings tab; this is
a multi-pane authoring workspace. Scalar knobs (verbosity, sample rate,
retention, eval timeout) live in the existing admin settings area.

```
web/src/pages/admin-policy/
  AdminPolicyLayout.tsx      // sub-nav: Documents | Vendor | Decision Log
  PolicyDocumentList.tsx     // by domain; enabled toggle; active-version badge
  PolicyEditorPanel.tsx      // CodeMirror 6 editor + Validate + Save version + Activate
  PolicyVendorViewer.tsx     // read-only vendor modules
  PolicySimulatePanel.tsx    // input JSON (seeded example per decision type) + Run → result/trace
  PolicyVersionHistory.tsx   // versions, diff vs active, rollback
  PolicyDecisionLogTable.tsx // filterable, cursor-paginated
web/src/hooks/queries/admin/policy.ts
```

**Editor: CodeMirror 6** via `@uiw/react-codemirror` (~60–120 KB gz — the
first code-editor dependency in `web/`; decided deliberately). A ~60-line
hand-rolled `StreamLanguage` Rego mode (keywords, comments, strings/numbers)
provides highlighting; `/validate` responses map to `@codemirror/lint`
diagnostics so compile errors render as inline squiggles at the reported
line/col. Save-as-version requires a passing Validate; Activate is a separate
explicit action — nothing goes live without compile-check + (by UI flow)
simulation.

## Vendor policy content

- `vendor/scope.rego` reproduces `access.Resolver.Resolve` (library
  intersection per `effectiveLibraries`, disabled-library subtraction,
  rating/quality min-merge, `profile_verified` passthrough).
- `vendor/permission.rego` reproduces `auth.HasEffectivePermission`,
  acting-admin gating (`actingAdminAllowed`), and the library-scoped
  `metadata_curation` check.
- `vendor/action.rego` reproduces `downloads` gating
  (`ensureTranscodeAllowed`, `PresetsFor` eligibility, quality/rating
  ceilings) and playback admission math against provided counts/limits.
- `vendor/lib/{ratings,quality}.rego` port the rank tables from
  `internal/access/rating.go` / `quality.go`. This is deliberate, test-pinned
  duplication until the Go tables are deleted in the cleanup phase.
- Rego unit tests (`*_test.rego`) run through `opa/v1/tester` inside
  `go test ./internal/policy/...` — no `opa` CLI dependency in CI.

## Rollout & parity strategy

Direct replacement, staged per surface, gated by dual-execution parity tests:

1. Engine + vendor Rego + parity suite land as dead code (nothing wired).
2. Parity tests run the *same fixtures* through the legacy Go function and
   the PDP, asserting identical output — including a combinatorial battery
   over `account × profile × disabled` library-set shapes targeting the
   nil-vs-empty boundary.
3. Cutover PRs swap constructors behind unchanged interfaces. **All five**
   `access.NewResolver` sites cut over for the scope surface:
   `internal/api/router.go` (viewer middleware), `cmd/silo/main.go`
   (notifications scopes, reconciler, jellycompat filter), and
   `internal/audiobooks/access_resolver.go` (ABS).
4. Legacy implementations (`access/resolver.go` logic, `rating.go`,
   `quality.go` tables, downloads/permission inline checks) stay compiled and
   tested for one release as the reference implementation, then are deleted in
   a cleanup PR. `access.Scope`/`ResolveInput` types and `profile_token.go`
   remain permanently (wire contract + crypto).
5. A CI-visible benchmark asserts scope-resolution p99 (eval + input
   marshaling) stays under 200 µs.

## Risks

- **Direct replacement has no production fallback.** The safety net is
  entirely the parity suite; a fixture gap ships live. Mitigations: staged
  per-surface cutover (each PR independently revertable), legacy code retained
  one release, always-logged denials making regressions observable quickly.
- **Nil-vs-empty library-set round-trip** is the highest-risk single detail;
  addressed with the explicit `unrestricted` field + combinatorial tests.
- **Admin foot-guns:** narrowing-only merge prevents accidental widening, but
  a bad override can still lock a household out (deny-everything). Mitigations:
  simulate-before-activate UI flow, one-click rollback, the `enabled` kill
  switch, and vendor behavior as the guaranteed floor when the custom document
  is disabled.
- **Eval-timeout DoS surface:** a pathological custom policy costs up to
  25 ms per decision server-wide until rolled back. Accepted for v1 (editor is
  acting-admin-only); a repeated-timeout circuit breaker is a follow-up
  candidate.
- **Hand-rolled Rego highlighting** will lag full Rego syntax; accepted (it
  is cosmetic; validation is server-side).
- **Playback admission counting stays in Go** — the policy decides given
  counts, so "unify authz" is unification of *decisions*, not of session
  bookkeeping. Documented as intentional.

## Deferred decisions

- Elevated (widening) override path — needs its own product decision and a
  stricter gate.
- Compile-to-SQL data filtering — revisit once v1 has baked; requires the
  filtering-fragment policy style and a symbolic-store compile path.
- Plugin policy gating — candidate follow-up: a `DecisionName` for plugin
  route access consulted by `plugins.HTTPProxy`.
- Persisted regression suites (`policy_test_cases`) and
  simulate-against-recent-decisions rollback preview.
