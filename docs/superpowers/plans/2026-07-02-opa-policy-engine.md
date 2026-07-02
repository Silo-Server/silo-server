# OPA Policy Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Commands assume the repository root is the cwd.

**Spec:** [`../specs/2026-07-02-opa-policy-engine-design.md`](../specs/2026-07-02-opa-policy-engine-design.md) — read it first; this plan does not restate the full design rationale.

**Goal:** Embed OPA (`github.com/open-policy-agent/opa/v1/rego`) as Silo's authorization engine: vendor Rego reproduces today's viewer-scope, permission-gate, and download/playback decisions exactly (proven by dual-execution parity tests), admins get a full policy editor (CodeMirror, validate, simulate, versioning, rollback) for narrowing-only custom overrides, and every decision feeds a partitioned audit log.

**Architecture:** One new `internal/policy` package (engine, typed PDP, Postgres-backed document store, compile sandbox, async decision logger, System lifecycle). Vendor Rego ships via `go:embed`; admin documents live in Postgres as immutable versions with an activate pointer and a global `policy_generation` counter; cross-node invalidation via a new `cache.EventPolicyChanged` on the existing `ChannelAdmin` plus a 60s poll fallback. Adapters implement the existing interfaces (`middleware.ViewerResolver`, permission middleware, downloads gate, `playback.SessionLimitProvider`) so cutover is a constructor swap per surface. Direct replacement, staged per-surface, no runtime toggle.

**Tech Stack:** Go (pgx, chi, `opa/v1/rego` + `opa/v1/ast` + `opa/v1/tester`), Goose migrations, `internal/partman`, Redis `cache.EventBus`, React/TypeScript + TanStack Query + CodeMirror 6 (`@uiw/react-codemirror`).

## Global Constraints

- **API additive-only within `/api/v1`** — new endpoints/fields only; capability endpoint for feature detection. (CLAUDE.md)
- **Performance first, reliability first** — prepared queries only; input-document-only (no OPA store mirroring, no DB reads inside eval); async decision logging that never blocks a decision; fail closed on every eval error/timeout.
- **Frozen contracts:** `access.Scope` / `access.ResolveInput` shapes; PIN/profile-token crypto stays in `internal/access/profile_token.go`; `access_policy_revision` semantics (library_ids changes do NOT bump it — `internal/auth/repository.go` has an explicit comment); jellycompat and API keys pass `SkipPINVerification: true`.
- **Narrowing-only overrides**, enforced in vendor Rego by tightening-direction merges (intersect libraries, min ceilings, AND booleans) — never by heuristic post-checks.
- **Sandbox invariants:** custom modules compile under stripped `ast.Capabilities` (no `http.send`, `net.*`, `opa.runtime`) at save, activate, AND reload; per-eval `context.WithTimeout` (default 25ms, setting `policy.eval_timeout_ms`); package path must be `silo_custom.<domain>`.
- **Migrations:** timestamped Goose files via `make migrate-create NAME=...`; never touch legacy numeric migrations.
- **One concern per PR**; Conventional Commit subjects; each PR links the OPA epic (`Part of #NNN` — create the epic issue before Phase 1 lands).
- Worktree builds need `GOWORK=off` and a stubbed `web/dist` (see worktree-build-quirks memory). DB-backed Go tests use the `SILO_TEST_DATABASE_URL` skip pattern.
- Before opening each PR: `cd web && pnpm run lint && pnpm run format:check`, `make verify-local-paths`, `make lint`.

---

## Verified Baseline (confirmed against the tree — work from this, not intuition)

1. **OPA is greenfield**: `go.mod` has no `open-policy-agent` dependency; no `.rego` files exist anywhere in the repo. `web/package.json` has no code-editor dependency.
2. **Five `access.NewResolver` construction sites** (all must cut over in Phase 7, not just the router):
   - `internal/api/router.go:342` — viewer middleware resolver.
   - `cmd/silo/main.go:1384` — notifications scopes.
   - `cmd/silo/main.go:1813` — reconciler resolver.
   - `cmd/silo/main.go:2271` — jellycompat `AccessFilterFn` via `jellycompat.NewScopeAccessFilter`.
   - `internal/audiobooks/access_resolver.go:24` — ABS access resolver (constructed with `tokens = nil`; PIN verification intentionally skipped there).
3. **`middleware.ViewerResolver`** (`internal/api/middleware/viewer_access.go:14`) is a 1-method interface: `Resolve(ctx, access.ResolveInput) (access.Scope, error)`. The middleware maps `ErrProfileUnverified`→403, `ErrProfileNotFound`→404, anything else→500. Fail-closed already.
4. **Scope semantics:** `AllowedLibraryIDs == nil` means unrestricted; empty slice means "nothing". `DisabledLibraryIDs` only set when `AllowedLibraryIDs` is nil (`internal/access/resolver.go:95-106`). Profile PIN token must match `UserID`, `SessionID`, `ProfileID`, AND `PolicyRevision` (`resolver.go:88`).
5. **`RequireAdmin` is dead in production routing** — only `RequireActingAdmin` (`internal/api/middleware/auth.go:184`) is wired. Model one admin tier.
6. **Permissions today:** exactly two assignable permissions, `marker_edit` + `metadata_curation` (`internal/auth/permissions.go:13-16`); `HasEffectivePermission` grants all to enabled admins. `RequireMetadataCurationForItem` (`internal/api/middleware/permissions.go:44`) additionally requires every library containing the item to be inside `users.library_ids`.
7. **Downloads gating:** `internal/downloads/policy.go` — `ensureTranscodeAllowed(user, cfg)` (line 166), `DownloadQualityResolver.PresetsFor(user, cfg, artifactsAvailable)` (line 119), driven by `users.download_allowed` / `download_transcode_allowed` + `config.DownloadConfig`. Capability surface: `internal/api/handlers/downloads.go` `HandleCapability` (line 163).
8. **Playback limits:** `playback.SessionLimitProvider = func(ctx, userID) (SessionLimits, error)` (`internal/playback/session.go:115`), installed via `SessionManager.SetLimitProvider` (line 141). Live admission counting is in-memory inside `SessionManager` — Go keeps the counting; policy decides given counts.
9. **Log-table pattern to copy:** `opsPM := partman.NewManager(pool, "operational_logs", partman.Daily, 3)` (`cmd/silo/main.go:174`), `activityPM := partman.NewManager(pool, "activity_log", partman.Weekly, 2)` (`main.go:1629`); cleanup tasks registered via `taskMgr.Register(...)` (~`main.go:1710-1875`). Migration `migrations/sql/028_log_partitioning.sql` shows the PARTITION BY RANGE + DEFAULT partition idiom. `internal/opslog/repo.go` has the cursor-pagination shape to mirror.
10. **Event bus:** `cache.ChannelAdmin = "silo:admin"`, `EventSettingsChanged = "settings_changed"` (`internal/cache/redis.go:24,49`). No `EventPolicyChanged` exists yet. `internal/nodeconfig/watcher.go` shows the subscribe + 60s poll-fallback idiom.
11. **System lifecycle template:** `notifications.System` construction at `cmd/silo/main.go:1382-1409` (Start/Wait, wired into `api.Dependencies`).
12. **Admin routing:** `/admin` route group with `requireActingAdmin` in `internal/api/router.go` (~line 2259+); `Dependencies` struct at `router.go:78-187`. Capability-endpoint precedent: `internal/api/handlers/downloads.go` `HandleCapability`.
13. **Frontend:** admin nav registry `web/src/lib/adminNavigation.ts`; dedicated-page precedent (`/admin/logs`, `/admin/tasks`); query-hook shape `web/src/hooks/queries/admin/settings.ts`; query key factory `web/src/hooks/queries/keys.ts`; DTOs in `web/src/api/types.ts`; jsdom localStorage needs the per-file in-memory stub (see `web/src/api/client.test.ts`).
14. **Settings:** flat `server_settings` via `catalog.SettingsStore`; per-key validation switch in `internal/api/handlers/admin.go` `HandleUpdateSetting` (~line 2075+); typed parsing in `internal/config/db_loader.go`; restart-required registry `internal/config/restart_keys.go` (policy keys must all hot-reload — add none there). Policy Rego is not a secret: no `SensitiveSettingKeys` entries.

## Design Decisions (summary — details in the spec)

- **D1** One package `internal/policy`; nothing else imports `opa/v1/*`. Typed PDP methods (`ResolveViewerScope`, `CheckPermission`, `CheckAction`) over three prepared queries (`data.silo.{scope,permission,action}.decision`).
- **D2** Vendor Rego via `go:embed`, never in the DB. Admin documents: `policy_documents` + immutable `policy_document_versions` + `active_version_id` pointer + single-row `policy_generation` counter.
- **D3** Override contract: vendor computes `base_decision`, then `decision := data.silo_custom.<domain>.override(base_decision, input) else base_decision`, merged tightening-only in vendor Rego. Custom package path `silo_custom.<domain>` enforced at compile-check.
- **D4** `ScopeDecision` carries an explicit `unrestricted` bool; the adapter maps it to nil `AllowedLibraryIDs`. Combinatorial parity tests own this boundary.
- **D5** Decision log: dedicated partitioned `policy_decisions` (daily), async buffered writer, digest-by-default verbosity, 1-in-50 scope sampling, denials/errors always logged, retention task.
- **D6** New `cache.EventPolicyChanged` on `ChannelAdmin` + poll fallback on `policy_generation`.
- **D7** Frontend: dedicated `/admin/policy` page; CodeMirror 6 with hand-rolled Rego StreamLanguage + `@codemirror/lint` diagnostics from `/validate`.
- **D8** Direct replacement staged per surface; legacy Go logic retained (compiled + tested) for one release post-cutover, then deleted.

---

## Phase 1 — `internal/policy` engine core + vendor scope Rego + parity suite (PR 1, dead code)

- [ ] Add dependency: `go get github.com/open-policy-agent/opa@latest` (v1 module path; confirm `opa/v1/rego`, `opa/v1/ast`, `opa/v1/tester` import cleanly). Note binary-size delta in the PR body (~+19 MB expected).
- [ ] `internal/policy/input.go`: `ScopeInput`/`ScopeDecision` structs (JSON tags per spec, `schema_version`, request-context fields `request_time`/`device_id`/`client_ip`/`is_api_key`). Doc comments — these render in the editor reference panel later.
- [ ] `internal/policy/errors.go`: `ErrPolicyEvalFailed`, `ErrUnknownDecision`, `ErrCompileFailed` (wraps structured `{Row, Col, Message}` list).
- [ ] `internal/policy/vendor/lib/quality.rego` + `lib/ratings.rego`: port rank tables + `min`/`allowed`/`normalize` helpers from `internal/access/quality.go` and `rating.go`. Byte-for-byte table parity asserted by test (Task below).
- [ ] `internal/policy/vendor/scope.rego` (`package silo.scope`, `import rego.v1`): reproduce `access.Resolver.Resolve` — `effectiveLibraries` intersection logic, disabled-library subtraction (both restricted/unrestricted branches), quality min-merge, rating passthrough, `profile_verified` passthrough, explicit `unrestricted` output. Include the `data.silo_custom.scope.override(base_decision, input)` extension hook with tightening-only merge (intersect `allowed_library_ids`, union `disabled_library_ids`, min ceilings, AND `profile_verified`).
- [ ] `internal/policy/vendor/scope_test.rego`: one Rego test per branch of `Resolve` (no profile / account-restricted / profile-restricted / both-intersect / disabled-subtraction / unverified profile), plus override-merge tests proving a widening override has no effect.
- [ ] `internal/policy/vendor.go`: `//go:embed vendor` FS + module loader.
- [ ] `internal/policy/engine.go`: `Engine` with `queries map[DecisionName]rego.PreparedEvalQuery`, RWMutex atomic `swap`, `Evaluate(ctx, name, input, out) (Meta, error)` with `context.WithTimeout` (25ms default), fail-closed on err/empty result/decode failure. Vendor-only compile path for now (no DB).
- [ ] `internal/policy/compile.go` (first half): `LockedCapabilities()` stripping `http.send`, `net.*`, `opa.runtime`, `rego.parse_module` from `ast.CapabilitiesForThisVersion()`. (Custom-doc compile-check lands Phase 2; capabilities are needed now for the sandbox tests.)
- [ ] `internal/policy/pdp.go`: `PDP.ResolveViewerScope(ctx, ScopeInput) (ScopeDecision, Meta, error)` only (other methods come with their surfaces).
- [ ] `internal/policy/vendor_rego_test.go`: run all `vendor/*_test.rego` via `opa/v1/tester` inside `go test` (no CLI dependency).
- [ ] **Parity tests** `internal/policy/scope_parity_test.go`: table-driven fixtures run through BOTH `access.Resolver`-equivalent logic (drive the real resolver with stub repos, mirroring `internal/access` existing tests) and `PDP.ResolveViewerScope`, asserting identical `access.Scope`. Include the combinatorial battery: `account ∈ {nil, [], [1,2,3]} × profile ∈ {absent, unrestricted, [], [2,3,4]} × disabled ∈ {[], [2]} × verified ∈ {true,false}`.
- [ ] Sandbox test: a module using `http.send` fails to compile under `LockedCapabilities()`; a `while`-style pathological comprehension trips the eval timeout and returns `ErrPolicyEvalFailed`.
- [ ] Benchmark `internal/policy/engine_bench_test.go`: prepared-query scope eval incl. input marshaling; assert well under 200µs p99 locally; record the number in the PR body.
- [ ] Verify: `GOWORK=off go build ./... && GOWORK=off go test ./internal/policy/...` green; `make lint` green. **No wiring into router/main — dead code by design.**

## Phase 2 — Data model + PolicyStore + compile-check pipeline (PR 2)

- [ ] `make migrate-create NAME=policy_foundation`: `policy_documents`, `policy_document_versions` (immutable, `UNIQUE(document_id, version_number)`, deferred FK for `active_version_id`), `policy_generation` (single-row, seeded), `policy_decisions` (PARTITION BY RANGE on `"timestamp"` + DEFAULT partition + indexes incl. partial `WHERE allowed = false`) — schema per spec. Goose Up/Down both present.
- [ ] `internal/policy/store.go`: `PolicyStore` — CRUD for documents/versions, `ActiveSources(ctx) map[domain][]source`, `Activate(documentID, versionID)` bumping `policy_generation` in the same tx (`UPDATE ... SET generation = generation + 1 ... RETURNING`), `Generation(ctx)`, enable/disable, delete-guard (only when no active version).
- [ ] `internal/policy/compile.go` (second half): `CompileCheck(ctx, domain, source) error` — parse with locked capabilities, enforce `package silo_custom.<domain>`, compile candidate layered over vendor + other active docs, 2s compile budget, structured errors.
- [ ] Extend `Engine` load path: vendor (full caps) + enabled custom actives (locked caps); a custom doc failing compile at load is skipped with WARN, never fatal. `revision` = generation from store.
- [ ] DB-backed tests (`SILO_TEST_DATABASE_URL` skip pattern): store CRUD, activation atomicity under concurrent activates (no lost generation bump), version immutability, delete-guard.
- [ ] Verify: migration applies cleanly via `make migrate-up` on a scratch DB; `GOWORK=off go test ./internal/policy/...`.

## Phase 3 — System lifecycle, hot reload, cross-node invalidation (PR 3)

- [ ] `internal/cache/redis.go`: add `EventPolicyChanged = "policy_changed"` beside `EventSettingsChanged`.
- [ ] `internal/policy/system.go`: `System` (mirrors `notifications.System`) — `NewSystem(pool, eventBus, settingsReader)`; `Start(ctx)` does initial `reloadFromStore` (vendor-only compile failure = fatal; custom failure = degraded WARN), subscribes to `ChannelAdmin` for `EventPolicyChanged`, runs 60s poll fallback comparing `policy_generation` to loaded generation (idiom: `internal/nodeconfig/watcher.go`); graceful stop.
- [ ] Settings: `policy.eval_timeout_ms` (default 25) parsed in `internal/config/db_loader.go`, hot-applied via atomic value read by `Engine` — NOT in `restart_keys.go`.
- [ ] Wire in `cmd/silo/main.go` (near notifications wiring ~1382): construct `policy.System`, `Start(appCtx)`, add `deps.PolicySystem` field to `api.Dependencies`, deferred stop in shutdown ordering. Skip construction in proxy/transcode standalone modes.
- [ ] Cross-node test: two `System` instances over one test DB + EventBus double; activate on A, assert B converges via event AND (separately, with events suppressed) via poll fallback.
- [ ] Verify: `GOWORK=off go build ./...`; integrated-mode boot smoke (`make dev-backend` locally) shows policy system start log; no behavior change anywhere (still nothing querying the PDP in request paths).

## Phase 4 — Decision logging (PR 4)

- [ ] `internal/policy/decisionlog.go`: `DecisionLogger` — non-blocking buffered channel + batch-insert flush goroutine (shape: `internal/activitylog` writer/consumer); drop-and-count metric on full buffer; fields per spec (`decision_name`, `policy_generation`, identity, `allowed`, `eval_time_ns`, `input_digest`, verbosity-gated `input_sample`/`result_sample`, `error`).
- [ ] Sampling/verbosity settings (hot-reloaded, parsed in `db_loader.go`): `policy.decision_log_verbosity` (`digest`|`verbose`), `policy.decision_log_scope_sample_rate` (default 50 ⇒ 1-in-50), `policy.decision_log_retention_days` (default 14). Denials + eval errors bypass sampling always.
- [ ] Hook `PDP` methods to emit entries post-decision (never on the eval critical path — enqueue only).
- [ ] `internal/policy/decisionlog_repo.go`: cursor-paginated `List` (filters: decision_name, user_id, allowed, time range) mirroring `internal/opslog/repo.go`.
- [ ] `cmd/silo/main.go`: `policyPM := partman.NewManager(pool, "policy_decisions", partman.Daily, 3)` + `EnsureFuturePartitions` (non-fatal), register `tasks.NewPolicyDecisionLogCleanupTask(...)` beside the other log cleanup tasks (`internal/taskmanager/tasks/`).
- [ ] DB-backed tests: writer batch/flush/drop behavior, repo pagination + filters, partition creation, cleanup task drops expired partitions.
- [ ] Verify: `GOWORK=off go test ./internal/policy/... ./internal/taskmanager/...`.

## Phase 5 — Admin HTTP API (PR 5)

- [ ] `internal/policy/simulate.go`: throwaway-bundle simulate (`domain`, optional candidate `source`, `decision_name`, raw `input`) → decision + `eval_time_ns` + optional `rego.Tracer` trace. Never touches the live engine; never writes a decision-log row.
- [ ] `internal/api/handlers/policy.go`: thin handlers per the spec's endpoint table — vendor viewer, documents CRUD, versions (create = CompileCheck + persist, 422 with `{errors:[{row,col,message}]}`), activate (= rollback), enabled toggle, delete-guard, `/validate`, `/simulate`, `/decisions` list + detail. DTOs follow existing handler naming (`policyDocumentResponse`, ...).
- [ ] Capability endpoint `GET /api/v1/policy/capability` (authenticated, non-admin — mirror `downloads.HandleCapability`): `{enabled, editor_available, decision_types, generation}`.
- [ ] Mount in `internal/api/router.go`: `/policy/capability` in the authenticated group; `r.Route("/policy", ...)` inside the acting-admin `/admin` group.
- [ ] Handler tests: validate/simulate happy + compile-error paths, activation flow publishes `EventPolicyChanged`, capability shape, non-admin gets 403 on admin routes.
- [ ] Verify: `GOWORK=off go test ./internal/api/...`; manual smoke: create → validate → save version → simulate → activate → decision generation bumps.

## Phase 6 — Frontend `/admin/policy` page (PR 6)

- [ ] Deps: `pnpm add @uiw/react-codemirror @codemirror/lint @codemirror/language` in `web/`. Record bundle-size delta in the PR body.
- [ ] `web/src/api/types.ts`: additive DTOs (`PolicyCapability`, `PolicyDocument`, `PolicyVersion`, `PolicyValidateResult`, `PolicySimulateResult`, `PolicyDecisionEntry`).
- [ ] `web/src/hooks/queries/admin/policy.ts` + entries in `web/src/hooks/queries/keys.ts`: `usePolicyCapability`, `usePolicyDocuments`, `usePolicyVersions`, `useValidatePolicy`, `useCreatePolicyVersion`, `useActivatePolicyVersion`, `useSimulatePolicy`, `usePolicyDecisions` (mutations invalidate document/version keys).
- [ ] `web/src/lib/regoLanguage.ts`: ~60-line `StreamLanguage` Rego mode (keywords `package import default if else not in every some as with contains`, `#` comments, strings/numbers).
- [ ] Pages per spec layout under `web/src/pages/admin-policy/`: `AdminPolicyLayout` (sub-nav Documents | Vendor | Decision Log), `PolicyDocumentList`, `PolicyEditorPanel` (CodeMirror + Validate → lint diagnostics at row/col + Save version + separate Activate with confirm), `PolicyVendorViewer` (read-only), `PolicySimulatePanel` (JSON input seeded with per-decision example + result/trace + eval µs), `PolicyVersionHistory` (list, client-side diff vs active, rollback confirm), `PolicyDecisionLogTable` (filters + cursor pagination + row expand).
- [ ] Register route in `web/src/App.tsx` under `/admin/*`; nav entry in `web/src/lib/adminNavigation.ts` (System group, ShieldCheck-style icon). Hide/disable via `usePolicyCapability` when the engine reports disabled.
- [ ] Settings additions: policy log verbosity/sample-rate/retention + eval timeout fields in the existing admin settings area (small section, `useSettingsForm` pattern) + `HandleUpdateSetting` validation cases for the new keys.
- [ ] Vitest: editor validate-flow (mock 422 diagnostics render), simulate hook, decision-log pagination (remember the jsdom localStorage stub).
- [ ] Verify: `cd web && pnpm run lint && pnpm run format:check && pnpm test`; manual walkthrough — author a schedule-based narrowing override, validate, simulate, activate, watch it in the decision log, roll back. Screenshots in the PR (UI-change convention).

## Phase 7 — Cutover surface 1: viewer scope (PR 7)

- [ ] `internal/access/policy_resolver.go`: `PolicyResolver` implementing `middleware.ViewerResolver` — owns the same repo lookups `access.Resolver` does today (user row, profile via userstore, disabled-library setting), performs PIN/profile-token verification in Go (identical claims checks incl. `PolicyRevision`), builds `ScopeInput` (`profile_verified` as fact), calls `PDP.ResolveViewerScope`, maps `unrestricted` → nil, returns `access.Scope` with `PolicyRevision`/`ProfileID`/`UserID` filled Go-side. Error taxonomy preserved: `ErrProfileNotFound`, `ErrProfileUnverified`, wrapped internals.
- [ ] Swap **all five** construction sites to the policy-backed resolver: `internal/api/router.go:342`, `cmd/silo/main.go:1384` (notifications), `main.go:1813` (reconciler), `main.go:2271` (jellycompat), `internal/audiobooks/access_resolver.go:24` (ABS — preserve its skip-PIN semantics via `tokens=nil` equivalent). Do NOT delete `access.Resolver`.
- [ ] Re-point/extend existing tests: middleware viewer-access tests and jellycompat scope tests must pass unchanged against the new resolver (that is the point). Parity suite from Phase 1 remains the gate.
- [ ] Verify: full `GOWORK=off go test ./...` (jellycompat `TestBeginWebOperation*` flakes are pre-existing); manual smoke on dev: restricted profile + PIN profile + disabled libraries behave identically across native web and a Jellyfin client; decision log shows sampled scope entries.

## Phase 8 — Cutover surface 2: permission gates (PR 8)

- [ ] `internal/policy/vendor/permission.rego` + tests: reproduce `HasEffectivePermission` (enabled + admin-grants-all + assigned list), acting-admin rule (`role==admin` AND (no declared profile OR declared profile primary)), `metadata_curation` item-scope rule (every target library ∈ user allowlist; empty allowlist = unrestricted). Override hook `silo_custom.permission.override`, AND-merge only.
- [ ] `internal/policy/input.go` + `pdp.go`: `PermissionInput`/`PermissionDecision`, `CheckPermission`.
- [ ] Parity tests against `auth.HasEffectivePermission`/`EffectivePermissions` and `actingAdminAllowed` fixtures.
- [ ] `internal/api/middleware/policy_gates.go`: acting-admin middleware + `RequireMetadataCurationForItem` equivalent backed by `CheckPermission` — same signatures as the existing constructors so `router.go` call sites are one-line swaps. Go still resolves the declared-profile-primary fact (DB lookup) before eval. Eval failure → 500 (matches existing `actingAdminAllowed` error contract); clean deny → 403.
- [ ] Swap wiring in `internal/api/router.go` (acting-admin group construction + permission middleware). Keep legacy middleware code compiled.
- [ ] Verify: existing middleware tests green against the new gates; manual: non-primary-profile admin still blocked from `/admin`, curator without library access still 403 on out-of-scope items.

## Phase 9 — Cutover surface 3: download/playback actions (PR 9)

- [ ] `internal/policy/vendor/action.rego` + tests: download rules (`download_allowed`; transcode requires `download_transcode_allowed` + artifacts; quality/rating ceilings via lib helpers) and playback admission (`current_active_streams < max_streams` when limit > 0; transcode analog; 0 = unlimited). Override hook `silo_custom.action.override`, tightening-only.
- [ ] `ActionInput`/`ActionDecision` + `PDP.CheckAction`; parity tests against `ensureTranscodeAllowed`, `PresetsFor`, and `SessionLimits` admission math.
- [ ] Downloads integration: `internal/downloads` gains a required `PolicyGate` dependency used by `Capability()`/`Create()`/preset resolution; map deny → existing `ErrDownloadNotAllowed`-family sentinels so `HandleCapability`/`writeDownloadError` responses are unchanged.
- [ ] Playback integration: reimplement the `SessionLimitProvider` closure (wired via `SetLimitProvider`) to consult `CheckAction` with Go-computed live counts; keep `SessionManager` counting untouched.
- [ ] Verify: downloads handler/service tests + playback session tests green; manual: user without download rights sees unchanged capability response; stream-limit enforcement unchanged; a custom action override (e.g. "no downloads 22:00–07:00") works end-to-end via the editor.

## Phase 10 — Bake, cleanup, docs (PR 10, next release)

- [ ] After one release of bake with all three surfaces cut over: delete legacy logic — `internal/access/resolver.go` resolve internals (keep `types.go`, `errors.go`, `context.go`, `profile_token.go`), `rating.go`/`quality.go` rank tables IF no non-policy callers remain (check `catalog.applyAccessFilter`'s `AllowedRatingsUpTo` usage — if catalog still needs the Go tables for SQL filtering, keep them and drop only dead paths; the SQL filter surface is explicitly deferred), legacy permission middleware, legacy downloads inline checks. Parity tests convert to golden tests against vendor Rego alone.
- [ ] Remove any temporarily duplicated fixtures; run `/simplify`-style pass on `internal/policy`.
- [ ] `docs/architecture/policy-engine.md`: subsystem overview, input-document contracts (source-of-truth tables), override-authoring guide with schedule/device examples, operational notes (generation, invalidation, decision-log knobs).
- [ ] Flag follow-up issues: repeated-timeout circuit breaker, persisted test suites, elevated (widening) override path, compile-to-SQL exploration, plugin gating.
- [ ] Verify: `GOWORK=off go build ./... && GOWORK=off go test ./...`, `make lint`, `make verify-local-paths`.

---

## Test & verification summary

| Gate | Command |
|---|---|
| Unit + Rego tests | `GOWORK=off go test ./internal/policy/...` |
| Parity (per surface, pre-cutover) | `GOWORK=off go test ./internal/policy/ -run 'Parity'` |
| DB-backed | `SILO_TEST_DATABASE_URL=... go test ./internal/policy/...` |
| Perf guardrail | `go test ./internal/policy/ -bench BenchmarkResolveViewerScope` (assert < 200µs incl. marshaling) |
| Lint/format | `make lint`; `cd web && pnpm run lint && pnpm run format:check` |
| Local-path hygiene | `make verify-local-paths` |

## Risks to watch during implementation

1. **nil-vs-empty `AllowedLibraryIDs`** — the `unrestricted` field mapping is the invariant; the combinatorial parity battery is the gate. Do not ship Phase 7 with any parity case skipped.
2. **Five call sites, not one** — missing the audiobooks/reconciler/notifications resolvers leaves silently divergent authz paths (the exact failure mode CLAUDE.md's duplicate-logic rule warns about).
3. **ABS resolver PIN semantics** — it constructs the resolver with a nil token validator today; the policy-backed replacement must preserve that (verified-by-construction), not accidentally start requiring profile tokens on ABS clients.
4. **Decision-log volume** — scope decisions fire per request; keep the 1-in-50 default and digest verbosity, and confirm the drop-counter metric is visible before Phase 7 ships.
5. **Vendor Rego drift vs Go tables** — until Phase 10 deletes the Go rank tables, the byte-parity test pinning `lib/{ratings,quality}.rego` to `internal/access/{rating,quality}.go` must stay green.
