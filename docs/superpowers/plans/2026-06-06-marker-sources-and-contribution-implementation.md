# Marker sources & TheIntroDB contribution — implementation plan (all phases)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Implement phases in order; each phase is independently shippable.

**Goal:** Implement the design in
[2026-06-06-marker-sources-and-contribution-design.md](../specs/2026-06-06-marker-sources-and-contribution-design.md):
fix the TheIntroDB read-path correctness gaps, formalize a multi-source provider model
(query-all / best-wins), and add the ability to contribute Silo's own markers back to
TheIntroDB — configured **per provider, off by default**. Server-side API + data model only;
web UI is a separate follow-up.

**Architecture:** Five sequential phases. Phase 1 fixes the existing provider (TVDB, real
confidence, best-candidate). Phase 2 adds a per-provider config table, per-segment marker
provenance, and `Registry.FetchMerged`. Phase 3 adds the submission client, the contribution
audit table, and the `ContributionService` engine. Phase 4 exposes the full admin API (manual
markers, provider config, contribute, history). Phase 5 adds the daily auto-contribution task.

**Tech Stack:** Go (chi handlers, pgx repositories), PostgreSQL (paired numbered migrations),
no frontend in this plan. Most touched packages (`internal/markers`, `internal/markers/introdb`,
`internal/taskmanager/tasks`) are pure Go; `internal/api/handlers` needs libvips (CGO image
deps). Tests run in a throwaway Go container (host has no Go toolchain).

Commands assume the repository root is the cwd.

---

## Phase overview & dependencies

| Phase | Delivers | Depends on | New migrations |
|------|----------|-----------|----------------|
| 1 | TheIntroDB read-path correctness (TVDB, real confidence, best-candidate) | — | none |
| 2 | `marker_provider_config` + per-segment provenance + `FetchMerged` | 1 (real confidence) | `marker_provider_config` |
| 3 | Submission client + `marker_contributions` + `ContributionService` | 2 (`Submitter`, provider config) | `marker_contributions` |
| 4 | Admin API: manual markers, provider config, contribute, history | 3 (service) | none |
| 5 | Daily auto-contribution task | 3 (service), 4 (optional) | none |

Phase 1's line-level steps are also captured standalone in
[2026-06-06-marker-introdb-readpath-correctness.md](2026-06-06-marker-introdb-readpath-correctness.md);
they are summarized here so this document is self-contained.

---

## Running tests (host has no Go toolchain)

Pure-Go packages (`internal/markers`, `internal/markers/introdb`, `internal/taskmanager/...`):

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/markers/... -v
```

`internal/api/handlers` (Phase 4) needs libvips:

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  sh -c 'apt-get update >/dev/null && apt-get install -y --no-install-recommends libvips-dev >/dev/null && go test ./internal/api/handlers/ -run TestAdminMarkers -v'
```

Whole-project compile: `go build ./...` (same container).

---

## Phase 1 — TheIntroDB read-path correctness

**Goal:** Honor TVDB ids, capture/use real per-segment confidence + submission_count, and pick
the best candidate among multiple. Contained to `internal/markers/introdb` plus one field on
`markers.Marker`. No migration, no write-path change.

**Files:** `internal/markers/introdb/{types.go,client.go,provider.go}` (modify),
`internal/markers/types.go` (modify, add `Marker.SubmissionCount`),
`internal/markers/introdb/{client_test.go,provider_test.go}` (create).

### Task 1.1 — Type plumbing
- [ ] Add `Confidence *float64` and `SubmissionCount *int` to `segmentTimestamps`
  (`introdb/types.go`); add `const defaultConfidence = 0.9`.
- [ ] Add `SubmissionCount int` to `markers.Marker` (`markers/types.go`).
- [ ] `go build ./internal/markers/...` → exit 0. Commit.

### Task 1.2 — TVDB lookups (gap #1)
- [ ] Create `introdb/client_test.go` (httptest): asserts `tvdb_id` sent when only tvdb given,
  tmdb preferred over tvdb/imdb, movie tvdb, and cache collapses repeat calls. Run → FAIL (compile).
- [ ] `client.go`: `FetchEpisode`/`FetchMovie` gain a `tvdbID` arg; query preference
  `tmdb → tvdb → imdb` via a `switch`; the empty-id guard includes tvdb; `cacheKeyEpisode`/
  `cacheKeyMovie` include a `tvdb:` branch.
- [ ] `provider.go` `FetchMarkers`: read `req.ExternalIDs[markers.ExternalIDKeyTVDB]`, include it
  in the empty guard, pass it to both client calls.
- [ ] Run client tests → PASS. Commit.

### Task 1.3 — Real confidence + best-candidate (gaps #2, #3)
- [ ] Create `introdb/provider_test.go`: TVDB-only resolves; real confidence used; default when
  absent; most-submitted candidate wins (ties broken by confidence). Run → FAIL.
- [ ] Rewrite `pickMarker` to use real confidence (fallback `defaultConfidence`), set
  `SubmissionCount`, and select the candidate with the highest `(submission_count, confidence)`.
- [ ] Run → PASS. Commit.

### Task 1.4 — Regression
- [ ] `go test ./internal/markers/... && go build ./...` → all green (existing `write_test.go`/
  `types_test.go` unaffected; write path untouched). Commit if needed.

(Full code for every step is in the standalone Phase 1 plan linked above.)

---

# Phase 2 — Per-provider config, per-segment provenance, FetchMerged

**Goal:** Add the `marker_provider_config` table that drives which providers are queried (and,
later, submitted to); make the marker write path carry **per-segment** provider/confidence/
algorithm so a merged multi-provider result writes correct provenance; add
`Registry.FetchMerged` (query all enabled providers, keep the best candidate per segment) and
switch the lazy-playback path to it. With only TheIntroDB enabled, behavior is unchanged.

**Files:**
- Create: `migrations/<next>_marker_provider_config.{up,down}.sql`
- Create: `internal/markers/provider_config.go`, `internal/markers/provider_config_test.go`
- Modify: `internal/markers/types.go` (`Marker.ProviderID`/`Algorithm`; `Registry.FetchMerged`)
- Modify: `internal/markers/write.go` (per-segment provenance in `MarkerUpdatePayload`/`BuildUpdatePayload`)
- Modify: `internal/scanner/file_repo.go` (`MarkerUpdate` + `applySegmentPatch` per-segment provenance)
- Modify: the `markers.MarkerUpdatePayload` → `scanner.MarkerUpdate` conversion (in the lazy-markers handler/adapter)
- Modify: `internal/api/handlers/playback_lazy_markers.go` (call `FetchMerged`)
- Modify: `cmd/silo/main.go` (construct the config store; pass to the registry)

### Task 2.1 — Migration: `marker_provider_config`
- [ ] Write `migrations/<next>_marker_provider_config.up.sql` (use the next free number; latest is 180):

```sql
CREATE TABLE public.marker_provider_config (
    provider                  text PRIMARY KEY,
    fetch_enabled             boolean NOT NULL DEFAULT true,
    fetch_priority            integer NOT NULL DEFAULT 100,
    contribute_enabled        boolean NOT NULL DEFAULT false,
    contribute_auto_local     boolean NOT NULL DEFAULT false,
    contribute_min_confidence double precision NOT NULL DEFAULT 0.95,
    updated_at                timestamptz NOT NULL DEFAULT now()
);
INSERT INTO public.marker_provider_config (provider, fetch_enabled) VALUES ('introdb', true);
```

- [ ] `.down.sql`: `DROP TABLE public.marker_provider_config;`
- [ ] Commit.

### Task 2.2 — Provider config store
- [ ] Create `internal/markers/provider_config.go`:

```go
type ProviderConfig struct {
    Provider                string
    FetchEnabled            bool
    FetchPriority           int
    ContributeEnabled       bool
    ContributeAutoLocal     bool
    ContributeMinConfidence float64
}

// ProviderConfigStore is a cached read/write façade over marker_provider_config.
// Reads serve from an in-memory snapshot; Update writes through and refreshes.
type ProviderConfigStore struct { /* pool, mu, cache map[string]ProviderConfig */ }

func NewProviderConfigStore(pool *pgxpool.Pool) *ProviderConfigStore
func (s *ProviderConfigStore) Reload(ctx context.Context) error            // load all rows into cache
func (s *ProviderConfigStore) List() []ProviderConfig                       // snapshot copy
func (s *ProviderConfigStore) Get(provider string) (ProviderConfig, bool)
func (s *ProviderConfigStore) Update(ctx context.Context, p ProviderConfig) error // upsert + refresh
// EnabledForFetch returns fetch_enabled providers sorted by (fetch_priority asc, provider asc).
func (s *ProviderConfigStore) EnabledForFetch() []ProviderConfig
```

- [ ] Hot-reload: subscribe the store's `Reload` to the existing `cache.EventSettingsChanged`
  Redis channel (mirror the `introdb.api_key` reload wiring in `cmd/silo/main.go`), or add a
  dedicated `marker_provider_config_changed` event published by `Update`. Either is acceptable;
  document which.
- [ ] `provider_config_test.go`: with a real/pgxmock pool or an injected fake, assert
  `EnabledForFetch` filters disabled rows and sorts by priority. Commit.

### Task 2.3 — Per-segment marker provenance (write path)

This makes a merged result (intro from provider A, credits from provider B) persist the correct
per-segment `*_markers_provider` / `*_markers_confidence` / `*_markers_algorithm`. The DB columns
already exist; today `applySegmentPatch` applies one shared triple.

- [ ] Add to `markers.Marker`: `ProviderID string`, `Algorithm string` (so each segment carries
  its origin). `provider.go` (Phase 1) sets these from `ProviderID`/`Algorithm` per marker.
- [ ] Rework `markers.MarkerUpdatePayload` (`write.go`) from one shared `Confidence`/`Provider`/
  `Algorithm` to a per-segment struct, e.g.:

```go
type SegmentPayload struct { Start, End *float64; Provider *string; Confidence *float64; Algorithm string }
type MarkerUpdatePayload struct {
    Intro, Credits, Recap, Preview SegmentPayload
    Source string // shared source class (e.g. "online"); per-kind provider differs
}
```

- [ ] Update `BuildUpdatePayload` to fill each `SegmentPayload` from the matching `Marker`
  (provider/confidence/algorithm per segment). Update `write_test.go`
  (`TestBuildUpdatePayloadAggregatesConfidence` → assert per-segment confidence: intro 0.7,
  credits 0.9, recap 0.5).
- [ ] Read `applySegmentPatch` and the `MarkerUpdatePayload → scanner.MarkerUpdate` conversion
  first, then extend `scanner.MarkerUpdate` + `applySegmentPatch` so each segment's
  provider/confidence/algorithm come from that segment (not a shared field). `nextSharedMarkerAttribution`
  keeps populating the legacy shared `markers_source`/`markers_confidence` columns from the
  highest-confidence applied segment.
- [ ] `go test ./internal/markers/... ./internal/scanner/...` → green. Commit.

### Task 2.4 — `Registry.FetchMerged`
- [ ] Give `Registry` access to the enabled set (constructor takes a `ProviderConfigStore`, or
  `FetchMerged` takes an `[]ProviderConfig` snapshot). Add:

```go
// FetchMerged queries every fetch-enabled provider concurrently and keeps, per
// segment kind, the candidate with the highest (SubmissionCount, Confidence),
// breaking ties by the provider's fetch_priority. Per-segment ProviderID/
// Algorithm are preserved so the write path records correct provenance.
func (r *Registry) FetchMerged(ctx context.Context, req Request) (Result, bool, error)
```

Algorithm: fan out via goroutines (bounded) to enabled providers; collect `(ProviderConfig,
Result, error)`; for each `MarkerKind`, choose the best `Marker` across results; assemble a
`Result` whose `Markers` carry their winning provider's `ProviderID`/`Algorithm`. Log and skip
provider errors (like `FetchFirstHit`). Return `ok=false` if no segment found.

- [ ] `types_test.go`: add a merge test — two fake providers, one wins intro, the other wins
  credits by submission_count; assert the merged result has both with correct per-segment
  ProviderID. Keep the existing `FetchFirstHit` tests. Commit.

### Task 2.5 — Switch lazy path + wire store
- [ ] In `internal/api/handlers/playback_lazy_markers.go`, replace the `FetchFirstHit` call in
  `fetchOnlineMarkersForPlayback` with `FetchMerged`.
- [ ] In `cmd/silo/main.go`, construct `NewProviderConfigStore`, `Reload` it at startup, wire its
  hot-reload, and pass it to the registry. (TheIntroDB stays registered as today.)
- [ ] `go build ./... && go test ./internal/markers/...` → green. With only `introdb` enabled,
  `FetchMerged` returns the same result `FetchFirstHit` did — verify a provider test asserting
  single-provider parity. Commit.

---

# Phase 3 — Submission client, contribution tracking, service engine

**Goal:** Build the machinery to submit a marker to a provider, idempotently and audited —
without yet wiring a trigger. Adds the `Submitter` capability, the introdb submission/stats
client methods, the `marker_contributions` table, and the `ContributionService`.

**Files:**
- Modify: `internal/markers/types.go` (`Submitter`, `SubmissionRequest`/`SubmissionResult`/`UserStats`)
- Modify: `internal/markers/introdb/{types.go,client.go,provider.go}` (submit + stats; implement `Submitter`)
- Create: `migrations/<next>_marker_contributions.{up,down}.sql`
- Create: `internal/markers/contribution_repo.go`, `internal/markers/contribute.go` (+ tests)
- Modify: `cmd/silo/main.go` (construct the service)

### Task 3.1 — `Submitter` capability + DTOs
- [ ] Add to `internal/markers/types.go`:

```go
type SubmissionRequest struct {
    Kind          ItemKind
    ExternalIDs   map[string]string // tmdb/imdb (tvdb not accepted by /submit; tmdb required)
    SeasonNumber  int
    EpisodeNumber int
    Segment       MarkerKind
    Start, End    *time.Duration // nil start ok for intro/recap; nil end ok for credits/preview
    Duration      time.Duration
}
type SubmissionResult struct { ID string; Status string; Weight float64 } // status: pending|accepted|rejected
type UserStats struct { Total, Accepted, Pending, Rejected int; AcceptanceRate float64; CurrentStreak, BestStreak int }

type Submitter interface {
    Provider
    SubmitMarker(ctx context.Context, req SubmissionRequest) (SubmissionResult, error)
    FetchUserStats(ctx context.Context) (UserStats, error)
}
```

- [ ] Commit (compiles; no implementer yet).

### Task 3.2 — introdb submission client
- [ ] `introdb/types.go`: add the request/response shapes for `POST /v3/submit`
  (`tmdb_id`,`imdb_id`,`type`,`segment`,`season`,`episode`,`video_duration_ms`,`start_ms`,`end_ms`;
  response `{submissions:[{id,status,weight,...}]}`) and `GET /v3/user/stats`. Remove the
  "Submissions are intentionally not supported" package note.
- [ ] `introdb/client.go`: add `SubmitSegment(ctx, ...) (*submitResponse, error)` (POST,
  `Authorization: Bearer` **required** — return an error if `apiKey == ""`; honor
  `X-UsageLimit-Reset` on 429) and `FetchUserStats(ctx) (*userStats, error)` (GET).
- [ ] `introdb/provider.go`: implement `markers.Submitter` — map `SubmissionRequest` to the
  client call (apply the v3 null rules: intro/recap drop a zero start to `null`; credits/preview
  send `null` end when absent), translate `SubmissionResult`/`UserStats`.
- [ ] `client_test.go`/`provider_test.go`: httptest asserts the submit body shape, bearer header,
  and that an empty key errors before any HTTP call; stats parse. Commit.

### Task 3.3 — `marker_contributions` table + repo
- [ ] Migration `<next>_marker_contributions.up.sql` (per design §C4):

```sql
CREATE TABLE public.marker_contributions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    media_file_id      integer NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    provider           text    NOT NULL,
    segment_kind       text    NOT NULL,
    source             text    NOT NULL,
    submitted_start_ms bigint,
    submitted_end_ms   bigint,
    video_duration_ms  bigint,
    content_hash       text    NOT NULL,
    submission_id      uuid,
    status             text    NOT NULL,
    http_status        integer,
    error              text,
    submitted_at       timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (media_file_id, provider, segment_kind, content_hash)
);
CREATE INDEX marker_contributions_file_idx ON public.marker_contributions(media_file_id);
```

- [ ] `.down.sql`: drop the table.
- [ ] `internal/markers/contribution_repo.go`: `ContributionStore` with `AlreadySubmitted(ctx,
  fileID, provider, kind, contentHash) (bool, error)`, `Record(ctx, ContributionRow) error`
  (upsert on the unique key), `ListByFile(ctx, fileID) ([]ContributionRow, error)`. Add a
  `ContentHash(kind string, startMs, endMs, durationMs *int64) string` helper (stable SHA-256).
- [ ] Repo test (value-hash idempotency, list). Commit.

### Task 3.4 — `ContributionService`
- [ ] `internal/markers/contribute.go`:

```go
type ContributionService struct { /* registry, resolver, cfg *ProviderConfigStore, store *ContributionStore, log */ }

// ContributeFile submits the file's eligible markers to enabled submitter providers.
// opts may scope to one provider and/or specific segment kinds. Auto callers pass
// requireAutoLocal=true so only providers with contribute_auto_local participate.
func (s *ContributionService) ContributeFile(ctx context.Context, file *models.MediaFile, opts ContributeOptions) ([]ContributionOutcome, error)
```

Per provider (from `cfg.List()` filtered to `Submitter` + `contribute_enabled`, and
`contribute_auto_local` when `opts.Auto`):
1. **Eligibility (C2):** for each requested segment present on the file, require
   `source ∈ {scanner, manual}` (skip `online` — circular); resolve external IDs via the existing
   resolver (skip if none / tmdb required by `/submit`); for auto require `source=scanner`,
   `kind=intro`, `confidence ≥ contribute_min_confidence`.
2. **Idempotency:** compute `ContentHash`; skip if `AlreadySubmitted`.
3. **Submit:** `provider.(Submitter).SubmitMarker(...)`.
4. **Persist:** `store.Record(...)` with returned status/submission_id (or `status="error"`).

- [ ] `contribute_test.go` with a fake `Submitter` + in-memory store: asserts online-sourced
  markers are skipped, sub-threshold auto markers skipped, duplicates skipped, a fresh manual
  marker submitted and recorded. Commit.

### Task 3.5 — Wire
- [ ] `cmd/silo/main.go`: construct `ContributionService` (registry, resolver, config store,
  contribution store). Not yet called by any handler/task. `go build ./...` + tests green. Commit.

---

# Phase 4 — Admin API (manual markers, provider config, contribute, history)

**Goal:** Expose the full backend contract under `RequireAdmin`. Mirror the structure of
`internal/api/handlers/admin_intro.go` (interface-typed deps, `chi.URLParam`, `writeError`/
`writeJSON`, `MarkerUpdateNotifier` after writes).

**Files:**
- Create: `internal/api/handlers/admin_markers.go` (+ test), `internal/api/handlers/admin_marker_providers.go` (+ test)
- Modify: `internal/api/router.go` (register routes in the `RequireAdmin` group)
- Modify: `cmd/silo/main.go` (construct + inject the handlers)

### Task 4.1 — Manual marker endpoints (design §C3)
- [ ] `admin_markers.go`:
  - `GET /admin/files/{fileId}/markers` → current per-segment values + provenance
    (`source`,`provider`,`confidence`,`algorithm`,`detected_at`) for intro/recap/credits/preview.
  - `PUT /admin/files/{fileId}/markers` → body per segment `{start,end}` (seconds) or `null` to
    clear; write `source = "manual"` via `scanner.FileRepository.UpsertMarkers` (priority 4);
    then `MarkerUpdateNotifier.MarkersUpdated(file)`. Validation: `end>start`; intro/recap may
    omit start; credits/preview may omit end; within `[0, duration]`.
  - `DELETE /admin/files/{fileId}/markers/{segment}` → clear one segment.
  - `GET|PUT /admin/items/{id}/markers` → resolve to the item's primary file (convenience).
- [ ] Handler test (libvips container): PUT writes manual markers and returns provenance;
  invalid range → 400; clear nulls the segment. Commit.

### Task 4.2 — Provider config + validate endpoints (design §C7)
- [ ] `admin_marker_providers.go`:
  - `GET /admin/markers/providers` → for each registered provider: id, `isSubmitter`, its
    `marker_provider_config` row, and (best-effort cached) `UserStats` for submitters with a key.
  - `PUT /admin/markers/providers/{provider}` → update the config row (validated; unknown → 404).
  - `POST /admin/markers/providers/{provider}/validate` → `Submitter.FetchUserStats`; non-submitter → 400.
- [ ] Test: list returns introdb with its config; update flips `contribute_enabled`; validate on
  a non-submitter → 400. Commit.

### Task 4.3 — Contribution endpoints (design §C7)
- [ ] In `admin_markers.go` (or a small `admin_contributions.go`):
  - `POST /admin/files/{fileId}/contribute` (optional body `{provider?, segments?[]}`) →
    `ContributionService.ContributeFile(...)`; returns per (provider, segment) outcome.
  - `GET /admin/files/{fileId}/contributions` → `ContributionStore.ListByFile`.
- [ ] Test with a fake service/store. Commit.

### Task 4.4 — Routing + wiring
- [ ] Register all new routes inside the existing `RequireAdmin` group in `internal/api/router.go`
  (mirror the `redetect-intro` registration). Inject the handlers in `cmd/silo/main.go`.
- [ ] `go build ./... && go test ./internal/api/handlers/ -run 'TestAdminMarkers|TestAdminMarkerProviders'`
  (libvips container) → green. Commit.

---

# Phase 5 — Daily auto-contribution task

**Goal:** A scheduled task that, for each provider with `contribute_enabled &&
contribute_auto_local`, submits high-confidence local intro detections that haven't been
contributed. Mirror `internal/taskmanager/tasks/detect_intro_markers.go`.

**Files:** Create `internal/taskmanager/tasks/contribute_markers.go` (+ test); modify
`cmd/silo/main.go` (register the task).

### Task 5.1 — `ContributeMarkersTask`
- [ ] Implement the `taskmanager.Task` interface (`Key="contribute_markers"`, `Name`,
  `Description`, `Category=TaskCategoryLibrary`, `IsHidden() bool`, `DefaultTriggers()` daily e.g.
  `"04:00"` — after the 03:30 detection task, `Execute`).
- [ ] `Execute`: short-circuit if no provider has `contribute_enabled && contribute_auto_local`
  (read `ProviderConfigStore`). Otherwise page through episode files with
  `intro_markers_source='scanner'` and `intro_markers_confidence ≥` the provider's
  `contribute_min_confidence` (a new `ContributionStore`/repo query, batched), and call
  `ContributionService.ContributeFile(file, ContributeOptions{Auto:true})`. Report progress; obey
  the introdb usage limit (the client already backs off on `X-UsageLimit-Reset`); idempotency
  makes interrupted runs resumable.
- [ ] Test with fakes: enabled+auto submits eligible files; disabled → no-op; sub-threshold
  skipped. Commit.

### Task 5.2 — Register
- [ ] Register the task where `DetectIntroMarkersTask` is registered in `cmd/silo/main.go`.
- [ ] `go build ./... && go test ./internal/taskmanager/...` → green. Commit.

---

## Final integration verification

**Files:** none (build + runtime).

- [ ] **Build the image** (compiles backend + frontend):
  ```bash
  docker build --build-arg BUILD_REVISION=$(git rev-parse --short HEAD) --build-arg BUILD_DIRTY=false -t silo-server:markers-test .
  ```
- [ ] **Recreate the container** (applies the new migrations on startup) and wait for health:
  ```bash
  docker compose up -d silo
  for i in $(seq 1 24); do s=$(docker inspect -f '{{.State.Health.Status}}' silo-silo-1); echo "$s"; [ "$s" = healthy ] && break; sleep 5; done
  ```
- [ ] **Verify migrations applied + tables exist:**
  ```bash
  docker compose exec -T postgres psql -U silo -d silo -c \
    "SELECT to_regclass('public.marker_provider_config'), to_regclass('public.marker_contributions');"
  docker compose exec -T postgres psql -U silo -d silo -c "SELECT * FROM marker_provider_config;"
  ```
  Expected: both tables present; an `introdb` row with `fetch_enabled=t, contribute_enabled=f`.
- [ ] **Smoke the read path (TVDB):** trigger playback (or call the marker fetch) for a
  TVDB-only-matched episode and confirm markers now populate where they previously did not.
- [ ] **Smoke contribution (manual, dry):** enable contribution for introdb against a **test/staging**
  TheIntroDB key, `PUT` a manual marker on a file, `POST /admin/files/{id}/contribute`, then
  `GET /admin/files/{id}/contributions` shows a `pending` row; a second contribute is idempotent
  (no new row). Use a non-production key so test data isn't submitted to the real database.
- [ ] `git status` → clean (all changes committed per phase).

---

## Self-review notes

- **Phase independence:** each phase compiles, tests, and ships alone; with only `introdb`
  enabled and contribution off (defaults), Phases 2–5 are observationally inert until an operator
  enables a provider — so they can land ahead of any behavior change.
- **Per-provider, off by default:** `contribute_enabled`/`contribute_auto_local` default `false`
  in the migration; the service refuses to submit otherwise; auto additionally gated on
  `contribute_auto_local`. The `provider` column on `marker_contributions` and the per-provider
  loop make multi-target submission first-class.
- **Circular-contribution guard:** `source='online'` is never contributed (C2 eligibility) — has
  a dedicated unit test in Task 3.4.
- **Idempotency:** value `content_hash` + the unique constraint mean repeat/interrupted runs
  don't double-submit, while a corrected value (new hash) does submit — matching TheIntroDB's
  weighted-average model.
- **Write-path accuracy:** Task 2.3 requires reading `applySegmentPatch` and the
  `MarkerUpdatePayload → scanner.MarkerUpdate` conversion before editing; the per-segment columns
  already exist in `media_files`, so this is a plumbing change, not a schema change.
- **Migration numbers:** `marker_provider_config` and `marker_contributions` take the next two
  free numbers at implementation time (181+; latest on disk is 180). Re-check for collisions
  before writing.
- **Out of scope (tracked in the design doc):** all web UI; per-user TheIntroDB accounts; a
  `marker_provider.v1` plugin capability. Clean seams left for each.
