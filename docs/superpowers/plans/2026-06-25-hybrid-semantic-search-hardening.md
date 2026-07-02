# Hybrid Semantic Search Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Commands assume the repository root is the cwd.

**Goal:** Make Meilisearch hybrid semantic search safe to enable broadly by gating it on *trustworthy* per-type vector coverage, validating embedder capability, and exposing per-query/admin diagnostics — without regressing the hot search path.

**Architecture:** Semantic search stays gated behind the existing `SemanticEnabled && wordCount >= 3` rule, plus a new **coverage gate** that reads an in-memory, periodically-refreshed per-type coverage snapshot (never a per-request DB query). Coverage = `current-model embeddings ÷ embed-eligible items`, computed entirely from Postgres and latched with hysteresis. Existing provider/fallback fields are plumbed through the resolver into an additive `search_diagnostics` response object. A rate-limited capability probe validates the Meili embedder without downing keyword search.

**Tech Stack:** Go (pgx/pgxpool), Meilisearch hybrid search (`userProvided` embedder), React/TypeScript admin UI, Goose migrations.

## Global Constraints

- **API additive-only within `/api/v1`** — never rename/remove a response field, change a type, or repurpose a status code. New fields are `omitempty` additions. (CLAUDE.md)
- **Performance first, reliability first, predictable under load** — the search request path must not gain a synchronous DB count, lock wait, or remote probe. Fail **safe** (keyword-only), never panic. (CLAUDE.md Core Priorities)
- **One additive index migration** is in scope (revised — see Assumptions): a btree on `media_item_embeddings(model)` to support the periodic coverage query. No schema/column changes.
- **Canonical embedding dimensions** = `embeddingvectors.CanonicalDimensions` (= 3072; re-exported as `recommendations.CanonicalEmbeddingDimensions`). Never hardcode the number.
- **Single source of truth for embed-eligibility** — the predicate `(status='matched' OR type IN ('audiobook','ebook'))` must not be duplicated. It moves to the neutral `internal/embeddingvectors` package (imported by both `catalog` and `recommendations`; `recommendations` already imports `catalog`, so `catalog` cannot import `recommendations`).
- **Single phase** — measurement *and* enforcement ship together. The diagnostics shipped here are what let us retune thresholds later without code changes-in-anger.
- Build/test in worktrees with `GOWORK=off` and a stubbed `web/dist` (see worktree-build-quirks memory).

---

## Corrected Baseline (what actually exists today — verified)

Work from this, not from intuition. Each point was confirmed against the tree.

1. **The only semantic skip rule is `SemanticEnabled && len(Fields(normalizedQuery)) >= 3`** (`internal/catalog/search_meilisearch_provider.go:329`). There is **no** `SkipTotal`/preview skip, and `TestMeilisearchSearchRequestBuildsHybridForApproximateInteractiveSearch` (`search_provider_test.go:213`) asserts hybrid **IS** used with `SkipTotal: true`. → **Do not add a `SkipTotal` skip.** Layer the coverage gate onto the existing rule; remove nothing.
2. **`CatalogSearchResult` already carries `Provider` and `FallbackReason`** (`search_provider.go:64-71`). Diagnostics work is *plumbing these through* `CatalogResult` (which drops them, `catalog_resolver.go:24-30,291-296`) — not inventing provider fields.
3. **The provider already self-downgrades hybrid→keyword on a Meili error** (`search_meilisearch_provider.go:221-228`). Diagnostics must report the *post-downgrade* reality.
4. **`countCatalogSearchVectorDocuments` ignores `model`** (`search_indexer.go:757`), counts the *indexed* population (no eligibility filter), and its companion denominator (`document_count`) comes from **Meili index stats** (`search_service.go:129`), not Postgres. It has **four callers** — `search_service.go:139`, `search_indexer.go:133`, `:203`, `:310` — all must be updated when its signature changes.
5. **Indexed population ≠ embed-eligible population.** `LoadDocumentsAfter` (`search_indexer.go:534`) indexes every type-matched, non-manga row regardless of `status`; embeddings are only written for `(status='matched' OR audiobook OR ebook)` (`recommendations/repo.go:154-160`, `recommendationItemEligibilityWhereClause`). A denominator of "indexable items" is therefore structurally < 1.0 on any library with unmatched video → **the gate would never open.** Coverage must use the **embed-eligible** population for both numerator and denominator.
6. **Status fields that already exist:** `document_count`, `vector_document_count` (`search_provider.go:347-348`). The earlier draft's `index_document_count` does **not** exist — keep real names; add new fields alongside.
7. **`UpsertEmbedding` already enqueues a Meili index event after every write** (`recommendations/repo.go:186`) — verified correct, **no change**.
8. **Catalog search settings are restart-bound** (`config/restart_keys.go:75-85`, *"Catalog search provider construction is intentionally startup-bound in v1"*). The service is built **once** in `NewRouter` and never live-rebuilt. → the coverage refresher is a single process-lifetime goroutine; no live-rebuild leak exists in v1.
9. **`catalogSearchService` is a local var in `NewRouter`** (`router.go:437`), wired into handlers but **never returned** to `main.go`. The long-lived `deps.AppContext` (`router.go:88`) is the refresher's context.
10. **Legacy `/search` plain-`q` re-route is descoped** — see [Descoped](#descoped--not-in-this-pass).

---

## Design Decisions

### D1 — Coverage = current-model embeddings ÷ embed-eligible items (single Postgres source, per type)

Let `ELIG(alias)` = `embeddingvectors.ItemEligibilityWhereClause(alias)` → `(alias.status='matched' OR alias.type IN ('audiobook','ebook'))` (moved here from `recommendations`; `recommendations.recommendationItemEligibilityWhereClause` is refactored to call it — no behavior change, no duplication).

- **Denominator** (embed-eligible items, per type):
  ```sql
  SELECT mi.type, COUNT(*) AS eligible
  FROM media_items mi
  WHERE NOT EXISTS (SELECT 1 FROM manga_chapters mc WHERE mc.chapter_content_id = mi.content_id)
    AND ($1::text[] IS NULL OR mi.type = ANY($1))
    AND (mi.status = 'matched' OR mi.type IN ('audiobook','ebook'))   -- ELIG("mi")
  GROUP BY mi.type
  ```
- **Numerator** (eligible items with a *current-model* embedding, per type):
  ```sql
  SELECT mi.type, COUNT(*) AS vectorized
  FROM media_item_embeddings e
  JOIN media_items mi ON mi.content_id = e.media_item_id
  WHERE NOT EXISTS (SELECT 1 FROM manga_chapters mc WHERE mc.chapter_content_id = mi.content_id)
    AND ($1::text[] IS NULL OR mi.type = ANY($1))
    AND (mi.status = 'matched' OR mi.type IN ('audiobook','ebook'))   -- ELIG("mi")
    AND ($2 = '' OR e.model = $2)
  GROUP BY mi.type
  ```

Applying `ELIG` to **both** guarantees numerator ⊆ denominator → ratio ∈ `[0,1]` (an embedding left behind on an item that became unmatched can't push a type over 1.0). Unmatched items are excluded from both, so they never drag coverage. As backfill completes, ratio → 1.0.

**Active model (`$2`)** is read from the embedding lock (`recommendations.EmbeddingLock.Model`, `embedding_lock.go:16`). No lock ⇒ `$2 = ""` and the snapshot is marked not-ready (never gate semantic on for an unestablished embedding space).

> **Why model-filtering is load-bearing:** right after a model change, old vectors persist but are mismatched to the new query embedder. A model-blind count reads ~100% at the most dangerous moment. Filtering by the locked model collapses coverage to ~0 until re-embedding catches up.

**Index (migration, in scope):** `media_item_embeddings` has only an HNSW index on `embedding` (`migrations/sql/001_schema.sql:1857`, `014_...:25`) — nothing on `model`. Add `CREATE INDEX CONCURRENTLY idx_media_item_embeddings_model ON public.media_item_embeddings (model)` so the numerator filter is index-supported. (Goose: timestamped file via `make migrate-create`, with `-- +goose NO TRANSACTION` because `CONCURRENTLY` cannot run in a txn.)

### D2 — Coverage lives in an in-memory snapshot, refreshed off the request path

A `semanticCoverageTracker` (new, `internal/catalog/semantic_coverage.go`) holds an `atomic.Pointer[semanticCoverageSnapshot]`.

- **Refresh cadence:** a single background goroutine every `semanticCoverageRefreshInterval` (**2 minutes** — long enough that two `GROUP BY` aggregates are negligible steady-state load even at 1M rows; short enough to bound the post-model-change danger window, which also fails safe via the model-collapse rule below). The interval is a named const; **there is no after-sync trigger** (the indexer and service share no handle — see Descoped rationale; the interval alone is sufficient and avoids a cross-component wiring + race).
- **Single-flight & immutability:** `Refresh` holds a `sync.Mutex` across read-prev → query → compute → publish (background path; contention irrelevant). Each refresh builds a **fresh** `PerType` map and publishes a new immutable snapshot pointer; the published snapshot is **never mutated** after publish (avoids a `map` data race against concurrent `CoverageReady` reads). A `-race` test fires refreshes concurrently with reads.
- **Fail-safe reads:** `CoverageReady` reads only the atomic pointer — **no DB, no lock, no remote call.** If the snapshot is `nil` (boot, before first refresh) it returns `(false, "coverage not yet computed")` — **never** dereferences a nil map (that would be a hot-path panic).
- **Error retention:** on a refresh query error, retain the last-good snapshot (transient DB blips must not flip semantic off); log; do not publish a zeroed snapshot. Admin status flags `UpdatedAt` staleness.
- **Model-change collapse:** the snapshot records the `Model` it was computed for. When `Refresh` observes the active model differs from the published snapshot's model, it **immediately publishes an all-not-ready snapshot** (safe) and recomputes real coverage under the new model on the same pass. This bounds the mismatched-vector window to ≤ one interval and fails safe within it.

### D3 — Per-type readiness with hysteresis; scope = AND of its types; empty scope = all types

Each type's `ready` flag is **latched**: flips **true** at `ratio >= semanticCoverageEnableRatio` (0.90), flips **false** only at `ratio < semanticCoverageDisableRatio` (0.80). The snapshot carries prior latched state so each refresh applies hysteresis — preventing flap (identical queries returning differently-ranked results).

`CoverageReady(itemTypes)`:
- **Non-empty scope:** ready iff **every** requested type is latched-ready (`min` semantics — a weak subtype blocks the mixed scope). The reason names the first failing type.
- **Empty/nil scope** (the common "search everything" case — `MediaScopeItemTypes("") → nil`, so `ItemTypes == nil` reaches the gate): ready iff **every type in the snapshot** is latched-ready. This deliberately mirrors the empty-config semantics and refuses to let an unscoped search bypass a weak type. Reason names the first failing type, or `"coverage not yet computed"` if the snapshot is nil/empty.

### D4 — Diagnostics: existing fields, plumbed, reported post-downgrade, scoped to the provider path

- Add `Mode string` (`"keyword"`/`"hybrid"`) and `SemanticUsed bool` to `CatalogSearchResult`; the Meili provider sets them to reflect the **final** request after the internal hybrid→keyword downgrade.
- Add `Provider`, `Mode`, `SemanticUsed`, `FallbackReason` to `CatalogResult`; copy them in `resolveDirectSearchSource`.
- Add an `omitempty` `search_diagnostics` object to `catalogResponse`.
- **Scope:** diagnostics are produced **only** on the direct provider-backed search path (`useDirectSearchPath` = relevance sort, desc, no advanced rules — `catalog_resolver.go:1416-1427`). They are **omitted** for: browse, non-relevance-sorted `q=` searches (which never hit the provider), and `group=work` responses (the grouped wrapper builds a fresh `CatalogResult` at `catalog.go:287-293` and is already approximate — `TotalExact=false`). Each exclusion gets an explicit handler test.

### D5 — Capability validation: narrow, rate-limited, non-fatal, admin-path only

Embedder settings are written by Silo itself, so validation guards only **external drift** (Meili volume reset, manual edits). Add `GetSettings` to the Meili client; verify the configured embedder exists, `source == "userProvided"`, and `dimensions == embeddingvectors.CanonicalDimensions`. The hybrid probe:
- runs **only on the admin status path** (`HandleGetCatalogSearchStatus`), **not** on `CheckConnection`/health (which only does `GET /health`), and the admin query has no `refetchInterval` (`settings.ts`, `staleTime: 15_000`);
- runs **only when `state.ActiveIndexUID != ""`**, and is **cached for `semanticCapabilityProbeTTL` (5m)** → ≤ 1 probe / 5m regardless of refresh rate;
- uses a **non-degenerate unit sample vector** (`v[0]=1.0`, rest 0, length `CanonicalDimensions`) — an all-zero vector has zero norm and can spuriously fail cosine hybrid; targets `ActiveIndexUID` with `Hybrid.Embedder = config.Embedder`, `limit:1`.

Failures surface in admin status and force keyword-only; they **never** flip provider `Healthy` or trip the circuit.

### D6 — Backfill split: cheap discovery vs. bounded text-staleness, with fairness

The expensive part of `ListEmbeddingTextCandidates` is **building `current_text` for every eligible row** via 5 `LATERAL` joins to `item_people` (`recommendations/repo.go:544-589`) — *not* the `IS DISTINCT FROM` comparison.

- **Cheap discovery** (`ListMissingOrModelStaleEmbeddingIDs`): `LEFT JOIN media_item_embeddings`, filter `ELIG("mi") AND (e.media_item_id IS NULL OR e.model = '' OR e.model <> $currentModel)`, `ORDER BY mi.content_id`, `LIMIT`. **No `LATERAL`, no `current_text`.** The SQL is built by a package-private `buildMissingOrModelStaleEmbeddingIDsSQL() string` helper so a unit test can assert it contains neither `item_people` nor `LATERAL` (mirrors the existing `embeddingEligibilityWhereClause()` string-helper test pattern). Returns IDs; `current_text` is then built **only for that bounded batch** via `BuildEmbeddingTextForIDs(ids)`.
- **Text-staleness** (existing `ListEmbeddingTextCandidates`): keep, but run as a **bounded, unconditional quota per cycle** (`embeddingTextStaleQuotaPerRun`) so canonical-text-stale items progress even when the cheap backlog never empties (fairness against starvation).
- **Cursors:** the cheap pass advances `afterID` from the **cheap query's** last id (it is the ordered cursor), independent of `BuildEmbeddingTextForIDs` row order (`ANY($1)` does not preserve slice order, and a dropped id must not desync the cursor). The text-stale pass keeps its **own** `afterID`. Both monotonic.

---

## File Structure

| File | Responsibility | Change |
| --- | --- | --- |
| `migrations/sql/<ts>_media_item_embeddings_model_index.sql` | `(model)` btree, `CONCURRENTLY` / `NO TRANSACTION` | Create |
| `internal/embeddingvectors/eligibility.go` | `ItemEligibilityWhereClause(alias)` — single source of truth | Create |
| `internal/catalog/semantic_coverage.go` | tracker, snapshot, querier seam, per-type SQL, hysteresis, single-flight refresher | Create |
| `internal/catalog/search_provider.go` | gate + model-provider interfaces; `Mode`/`SemanticUsed`; semantic status structs | Modify |
| `internal/catalog/search_meilisearch_provider.go` | consult gate; set `Mode`/`SemanticUsed` (incl. downgrade); capability probe (unit vector) | Modify |
| `internal/catalog/search_meilisearch_client.go` | `GetSettings` + settings DTOs | Modify |
| `internal/catalog/search_service.go` | build/own tracker; `StartCoverageRefresh`; comma-ok model-provider; `Status()` reads snapshot | Modify |
| `internal/catalog/search_indexer.go` | `ELIG`+`model` numerator & eligible denominator; update **all 4** `countCatalogSearchVectorDocuments` callers | Modify |
| `internal/catalog/catalog_resolver.go` | carry diagnostics through `CatalogResult` (direct path only) | Modify |
| `internal/recommendations/repo.go` | delegate eligibility to `embeddingvectors`; `ListMissingOrModelStaleEmbeddingIDs` (+SQL helper); `BuildEmbeddingTextForIDs` | Modify |
| `internal/recommendations/similar.go` | `EmbedAll`: cheap drain + unconditional text-stale quota; dual cursors | Modify |
| `internal/recommendations/engine.go` | `ActiveEmbeddingModel(ctx)` from the lock | Modify |
| `internal/api/handlers/catalog.go` | `searchDiagnostics` DTO; populate on direct path; omit for grouped | Modify |
| `internal/api/router.go` | start `service.StartCoverageRefresh(deps.AppContext)` inside `NewRouter` | Modify |
| `web/src/hooks/queries/admin/settings.ts`, `web/src/pages/admin-settings/SearchSettings.tsx` | surface readiness/ratio/per-type/capability | Modify |

---

## Tasks

### Task 1: Shared eligibility predicate, model+eligibility coverage counts, supporting index

**Files:** Create `internal/embeddingvectors/eligibility.go`, `migrations/sql/<ts>_media_item_embeddings_model_index.sql`; Modify `internal/recommendations/repo.go:150-160`, `internal/catalog/search_indexer.go:757-782` (+ callers `:133,:203,:310`, `search_service.go:139`); Test `internal/embeddingvectors/eligibility_test.go`, `internal/catalog/search_indexer_test.go`.

**Interfaces — Produces:**
- `embeddingvectors.ItemEligibilityWhereClause(alias string) string`
- `countCatalogSearchVectorDocuments(ctx, q coverageQuerier, itemTypes []string, model string) (int, error)` (adds `model`; applies `ELIG`)
- `catalogSemanticCoverageByType(ctx, q coverageQuerier, itemTypes []string, model string) ([]catalogTypeCoverage, error)` → `{Type string; Eligible, Vectorized int}`

- [ ] Migration: `make migrate-create NAME=media_item_embeddings_model_index`; body uses `-- +goose NO TRANSACTION` then a self-healing guard for a leftover invalid index from a crashed build (follow the `20260618164519_add_episodes_season_id_index.sql` precedent: a `DO $$ ... NOT i.indisvalid ... DROP INDEX $$` block), then `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_item_embeddings_model ON public.media_item_embeddings (model);` (down: `DROP INDEX CONCURRENTLY IF EXISTS`). The runner honors `NO TRANSACTION` (three shipping precedents). Run `make migrate-up`; verify with `make migrate-status`.
- [ ] Move the eligibility predicate to `embeddingvectors`; refactor `recommendationItemEligibilityWhereClause` to delegate (assert output string unchanged).
- [ ] Failing tests: (a) `model` filter excludes rows whose `e.model` differs; (b) an **unmatched, non-book** item inflates neither numerator nor denominator (the C1 regression guard); (c) denominator counts eligible items with no embedding row; (d) ratio ∈ [0,1] when a stale embedding exists on a now-unmatched item.
- [ ] Implement both queries with `ELIG`; merge per type in Go; update **all four** callers (pass active model where available, else `""`).
- [ ] `GOWORK=off go test ./internal/embeddingvectors ./internal/catalog -run 'Eligib|Coverage' -count=1` → PASS. Commit.

### Task 2: Active embedding model from the lock

**Files:** Modify recommendations `Engine` (`engine.go`), `internal/catalog/search_provider.go`; Test `internal/recommendations/*_test.go`.

**Produces:** `recommendations: func (e *Engine) ActiveEmbeddingModel(ctx) (string, error)` (→ `GetEmbeddingLock`; `lock.Model` or `""`); `catalog: type CatalogSemanticModelProvider interface { ActiveEmbeddingModel(ctx context.Context) (string, error) }`.

- [ ] Test: lock present → its `Model`; no lock → `("", nil)`.
- [ ] Implement; confirm `recEngine` satisfies both `CatalogSearchQueryVectorizer` (the real interface name, `search_provider.go:77`) and `CatalogSemanticModelProvider`.
- [ ] Run recommendations tests → PASS. Commit.

### Task 3: Coverage tracker — querier seam, snapshot, hysteresis, single-flight refresher

**Files:** Create `internal/catalog/semantic_coverage.go`, `semantic_coverage_test.go`; Modify `internal/catalog/search_provider.go` (consts + gate interface).

**Produces:**
```go
const (
    semanticCoverageEnableRatio     = 0.90
    semanticCoverageDisableRatio    = 0.80
    semanticCoverageRefreshInterval = 2 * time.Minute
)

// coverageQuerier is the seam so Refresh is unit-testable without a real *pgxpool.Pool.
type coverageQuerier interface {
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type catalogTypeCoverage struct { Type string; Eligible, Vectorized int; Ratio float64; Ready bool }
type semanticCoverageSnapshot struct { PerType map[string]catalogTypeCoverage; Overall float64; Model string; UpdatedAt time.Time }

type SemanticCoverageGate interface { CoverageReady(itemTypes []string) (ready bool, reason string) }

type semanticCoverageTracker struct { /* q coverageQuerier; indexTypes []string; models CatalogSemanticModelProvider; mu sync.Mutex; snap atomic.Pointer[semanticCoverageSnapshot]; clock func() time.Time */ }
func (t *semanticCoverageTracker) Refresh(ctx context.Context) error
func (t *semanticCoverageTracker) CoverageReady(itemTypes []string) (bool, string) // nil-snapshot ⇒ (false, "coverage not yet computed")
func (t *semanticCoverageTracker) Snapshot() *semanticCoverageSnapshot
func (t *semanticCoverageTracker) Run(ctx context.Context) // ticker; returns on ctx.Done()
```

- [ ] Test (no panic / fail-safe): `CoverageReady` on a freshly-constructed tracker (no `Refresh` yet) returns `(false, "coverage not yet computed")` and touches the querier **zero** times (querier stub fails the test if called).
- [ ] Test (hysteresis): 0.85→not ready; →0.92 ready; →0.85 **stays** ready; →0.79 not ready.
- [ ] Test (scope): `CoverageReady(["movie","series"])` false when `series` < threshold though `movie` ready; reason names `series`. Empty scope requires all snapshot types ready.
- [ ] Test (model collapse): snapshot computed for model A; active model becomes B → next `Refresh` publishes all-not-ready before recomputing.
- [ ] Test (`-race`): concurrent `Refresh` + `CoverageReady` loops; assert monotonic `UpdatedAt`, no race.
- [ ] Test (error retention): a `Refresh` whose query errors retains the prior snapshot.
- [ ] Implement single-flight mutex, fresh-map immutability, nil-safe reads, model-collapse, retain-last-good, ticker `Run`.
- [ ] `GOWORK=off go test ./internal/catalog -run Coverage -race -count=1` → PASS. Commit.

### Task 4: Wire gate + model provider; comma-ok degradation; start refresher in NewRouter

**Files:** Modify `internal/catalog/search_meilisearch_provider.go:27-66,329-334` (`MeilisearchProviderConfig` opens at `:27`), `internal/catalog/search_service.go:31-74`, `internal/api/router.go:437-552`.

**Consumes:** `SemanticCoverageGate`, `CatalogSemanticModelProvider`. **Produces:** `MeilisearchProviderConfig.Coverage SemanticCoverageGate`; `func (s *CatalogSearchService) StartCoverageRefresh(ctx context.Context)`.

- [ ] `shouldUseSemanticSearch` → `SemanticEnabled && wordCount>=3 && (p.config.Coverage == nil || ready)`; not-ready yields fallback reason `"semantic_not_ready: " + reason` (a **diagnostic, not an error**). Keep the `SkipTotal`-builds-hybrid test green.
- [ ] In `NewCatalogSearchServiceFromSettings`: only when `SemanticEnabled`, derive the model provider via **comma-ok** `mp, ok := queryVectorizer.(CatalogSemanticModelProvider)`. If `!ok` or `queryVectorizer == nil` (semantic enabled but recommendations disabled — a real config), build the tracker with a model provider that yields `""` ⇒ every type not-ready (reason `"embedding model provider unavailable"`); **never** assert without comma-ok (nil-interface assertion panics). Store the tracker on the service; pass it as `Coverage`.
- [ ] `StartCoverageRefresh` runs `tracker.Run(ctx)` once; no-op when there is no tracker (postgres/semantic-off). Call it from `NewRouter` after the service is built: `if catalogSearchService != nil { catalogSearchService.StartCoverageRefresh(deps.AppContext) }`. (The service is local to `NewRouter` and not returned; `deps.AppContext` is the process-lifetime ctx — Baseline #8/#9.)
- [ ] Test: stub gate not-ready ⇒ long query stays keyword-only; ready gate + vectorizer ⇒ hybrid emitted; nil vectorizer + semantic-enabled ⇒ not-ready, no panic.
- [ ] `GOWORK=off go test ./internal/catalog -count=1` → PASS. Commit.

### Task 5: Diagnostics plumbing (provider → resolver → handler), correctly scoped

**Files:** Modify `internal/catalog/search_provider.go:64-71`, `internal/catalog/search_meilisearch_provider.go:189-237`, `internal/catalog/catalog_resolver.go:24-30,269-297`, `internal/api/handlers/catalog.go:55-61,91-134`; Test `search_provider_test.go`, `catalog_test.go`.

**Produces:** `CatalogSearchResult{+Mode string; +SemanticUsed bool}`; `CatalogResult{+Provider,Mode string; +SemanticUsed bool; +FallbackReason string}`; handler `type searchDiagnostics struct { Provider, Mode string; SemanticUsed bool; FallbackReason string \`json:"fallback_reason,omitempty"\` }`; `catalogResponse{+SearchDiagnostics *searchDiagnostics \`json:"search_diagnostics,omitempty"\`}`.

- [ ] Provider: `Mode="hybrid", SemanticUsed=true` only when the hybrid request is issued **and survives**; on the downgrade (`:221-228`) set `Mode="keyword", SemanticUsed=false, FallbackReason=semanticFallback`.
- [ ] Resolver: copy the four fields in `resolveDirectSearchSource` (direct path only).
- [ ] Handler: populate `search_diagnostics` only on the non-grouped direct path; **omit** for `group=work` and for non-relevance-sort `q=` requests.
- [ ] Tests: (a) hybrid error ⇒ `semantic_used:false, mode:"keyword", fallback_reason` set (post-downgrade truth); (b) existing `total/total_exact/has_more/items/snapshot` byte-stable; (c) field **absent** for browse, for `group=work`, and for a `q=`+title-sort request.
- [ ] `GOWORK=off go test ./internal/catalog ./internal/api/handlers -count=1` → PASS. Commit.

### Task 6: Capability validation + rate-limited probe; admin status from snapshot

**Files:** Modify `internal/catalog/search_meilisearch_client.go:167+`, `internal/catalog/search_meilisearch_provider.go`, `internal/catalog/search_service.go:90-145`, `internal/catalog/search_provider.go` (status structs).

**Produces:**
```go
func (c *meilisearchClient) GetSettings(ctx, uid string) (meilisearchIndexSettings, error) // GET /indexes/{uid}/settings
type meilisearchIndexSettings struct { Embedders map[string]meilisearchEmbedderSettings `json:"embedders"` }
type meilisearchEmbedderSettings struct { Source string `json:"source"`; Dimensions int `json:"dimensions"` }
type CatalogSearchSemanticStatus struct {
    Ready bool `json:"ready"`; DisabledReason string `json:"disabled_reason,omitempty"`
    CoverageRatio float64 `json:"vector_coverage_ratio"`; CoverageUpdatedAt *time.Time `json:"coverage_updated_at,omitempty"`
    PerType []CatalogSearchTypeCoverage `json:"per_type,omitempty"`; Capability CatalogSearchSemanticCapability `json:"capability"`
}
type CatalogSearchTypeCoverage struct { Type string `json:"type"`; Eligible int `json:"eligible"`; Vectorized int `json:"vectorized"`; CoverageRatio float64 `json:"vector_coverage_ratio"`; Ready bool `json:"ready"` }
type CatalogSearchSemanticCapability struct { OK bool `json:"ok"`; Reason string `json:"reason,omitempty"`; Embedder string `json:"embedder,omitempty"`; Dimensions int `json:"dimensions,omitempty"` }
const semanticCapabilityProbeTTL = 5 * time.Minute
```

- [ ] Add `Semantic CatalogSearchSemanticStatus` to `CatalogSearchRuntimeStatus`. **Keep** `document_count`/`vector_document_count` unchanged (additive only). The `Semantic` block (ratio, per-type, ready) is read from `tracker.Snapshot()` — **no fresh query** in `Status()`; if no tracker, `Ready=false, DisabledReason="semantic disabled"`.
- [ ] Capability: embedder present, `source=="userProvided"`, `dimensions==embeddingvectors.CanonicalDimensions`; each failure → distinct `Reason`. Probe (unit vector, `limit:1`, `Embedder=config.Embedder`) only when `ActiveIndexUID != ""`, cached `semanticCapabilityProbeTTL`.
- [ ] Tests: missing embedder / wrong source / wrong dimensions / probe failure each distinct; provider `Healthy` and circuit untouched in all four.
- [ ] `GOWORK=off go test ./internal/catalog -count=1` → PASS. Commit.

### Task 7: Backfill cheap/expensive split with fairness

**Files:** Modify `internal/recommendations/repo.go:472-621`, `internal/recommendations/similar.go:251-362`; Test `repo_test.go`, `similar_test.go`.

**Produces:** `buildMissingOrModelStaleEmbeddingIDsSQL() string`; `ListMissingOrModelStaleEmbeddingIDs(ctx, afterID, currentModel string, limit int) ([]string, error)`; `BuildEmbeddingTextForIDs(ctx, ids []string) ([]EmbeddingTextCandidate, error)`; `EmbedAll` two-pass loop with dual cursors and an unconditional `embeddingTextStaleQuotaPerRun`.

- [ ] Test (SQL shape): `buildMissingOrModelStaleEmbeddingIDsSQL()` contains neither `"item_people"` nor `"LATERAL"`.
- [ ] Test (behavior, no real DB needed for the shape test; behavior tests use existing recommendations test seams): an item that is *only* canonical-text-stale (people changed, model current) is **not** returned by the cheap query; a missing/model-stale item **is**.
- [ ] Test (parity): `BuildEmbeddingTextForIDs(ids)` reproduces the same `canonical_text` as the legacy full CTE for the same ids, regardless of `ids` order.
- [ ] Test (no starvation): with a permanently non-empty cheap backlog, a text-stale item is still embedded within a bounded number of `EmbedAll` runs (quota is unconditional); both cursors stay monotonic.
- [ ] Implement; cheap `afterID` advances from the cheap query's last id.
- [ ] `GOWORK=off go test ./internal/recommendations -count=1` → PASS. Commit.

### Task 8: Admin UI surfacing

**Files:** Modify `web/src/hooks/queries/admin/settings.ts:21-54`, `web/src/pages/admin-settings/SearchSettings.tsx:202-261`.

- [ ] Extend the `CatalogSearchStatus` TS type with the `semantic` block (ready, disabled_reason, vector_coverage_ratio, coverage_updated_at, per_type, capability).
- [ ] Render a "Semantic readiness" row (ready/disabled + reason), overall coverage %, a per-type coverage table, a capability badge, and a "coverage updated" timestamp. Keep "Vectorized Documents" (`vector_document_count`).
- [ ] `cd web && pnpm run lint && pnpm run format:check` → PASS. Commit.

---

## Test Plan

**Go unit (`internal/embeddingvectors`, `internal/catalog`):**
- Eligibility predicate single-sourced; unmatched non-book item excluded from numerator **and** denominator (C1 guard); ratio ∈ [0,1].
- Hybrid omitted when gate not ready; emitted when ready + capable + long query + vectorizer present.
- **Model-stale safety:** only-old-model embeddings ⇒ coverage ~0 ⇒ keyword-only; model-change collapse publishes not-ready within one interval.
- Hysteresis latch; mixed-scope AND; empty-scope = all-types; nil-snapshot returns not-ready with **no panic** and **no DB call**; `-race` refresh/read; error retention.
- Capability: missing embedder / wrong source / wrong dimensions / probe failure each distinct; keyword stays up.

**Go unit (`internal/recommendations`):**
- Cheap SQL excludes `item_people`/`LATERAL`; people-only-stale row excluded from cheap pass; per-ID text build matches legacy output order-independently; no text-stale starvation; dual cursors monotonic.

**Go unit (`internal/api/handlers`):**
- `search_diagnostics` present on direct path; **absent** for browse, `group=work`, and non-relevance-sort `q=`; `semantic_used` reflects post-downgrade reality; existing fields byte-stable.

**Admin status:** `semantic.ready`, `disabled_reason`, `vector_coverage_ratio`, `per_type`, `capability`, `coverage_updated_at` sourced from the snapshot (no fresh query).

**Focused command:**
```bash
GOWORK=off go test ./internal/embeddingvectors ./internal/catalog ./internal/api/handlers ./internal/recommendations -race -count=1
```

---

## Descoped — not in this pass

**Routing legacy `/search?q=` through the provider pipeline.** Sunset **Wed, 01 Jul 2026** (`internal/api/handlers/legacy_read_routes.go:10`, `legacyReadSunset`) — days away; hardening a path scheduled for deletion is negative ROI. The typed legacy path already routes through the resolver; only the bare-`q` fallback hits `itemRepo.Search`, which the sunset removes. Re-routing also risks `browseResponse`↔`catalogResponse` shape drift. Reopen as a one-task follow-up **only if the sunset slips**, with a byte-for-byte `browseResponse` assertion.

**After-sync coverage refresh trigger.** The indexer (`main.go:1730`) and the search service (`router.go:457`) share no handle, so an after-drain trigger would need new cross-component wiring and would race the interval refresh. The 2-minute interval plus model-change collapse is sufficient; the trigger is intentionally omitted.

---

## Assumptions

- **One additive index migration** (`media_item_embeddings(model)`, `CONCURRENTLY`). No schema/column changes. *(Revised from the original "no migration": the model-filtered numerator is only cheap with this index, and performance is priority #1.)*
- All runtime coverage state is **in-memory** (atomic per-type snapshot), rebuilt on boot and refreshed every 2 minutes; not persisted.
- Coverage is computed **entirely from Postgres** over the **embed-eligible** population (`status='matched' OR audiobook/ebook`), model-filtered, manga excluded, restricted to configured index types; it **never** uses the Meili `document_count`.
- Active embedding model comes from `recommendations.EmbeddingLock.Model`; **no lock / no model provider ⇒ semantic not ready.**
- A model change is reflected within ≤ one refresh interval and **fails safe** within that window via the model-collapse rule; the residual ≤2-minute mismatch window is acceptable for a rare, admin-gated reconfiguration.
- Thresholds (enable 0.90 / disable 0.80) and the 2-minute interval are named consts and starting heuristics; the diagnostics shipped here are the instrument for retuning them. Promoting them to settings is a possible follow-up, not this pass.
- `semantic_ratio` stays `0.30` (`DefaultMeilisearchSemanticRatio`).
- Scope is hybrid-search hardening only — not conversational search.

---

## Self-Review (writing-plans checklist)

- **Findings coverage:** C1 eligibility denominator (D1, Task 1) · C2 empty-scope semantics (D3, Task 3) · missing `model` index (D1 migration, Task 1) · 4-caller signature change (Baseline #4, Task 1) · refresher wiring in `NewRouter`/`AppContext` (Baseline #9, Task 4) · concurrent-refresh single-flight + immutability + `-race` (D2, Task 3) · nil-snapshot/error fail-safe (D2, Task 3) · model-change collapse (D2, Task 3) · comma-ok nil vectorizer (Task 4) · querier seam + SQL-string helper for testability (Task 3, Task 7) · grouped/non-relevance diagnostics scoping (D4, Task 5) · unit probe vector + admin-only/5m (D5, Task 6) · `Status()` reads snapshot (Task 6) · backfill cursors + fairness (D6, Task 7) · legacy & after-sync descoped (Descoped) — all mapped.
- **Type consistency:** `SemanticCoverageGate`/`CatalogSemanticModelProvider`/`coverageQuerier`/`semanticCoverageTracker`/`catalogTypeCoverage`/`CatalogSearchSemanticStatus` used identically across tasks; `Eligible` (not `Indexable`) is the denominator field name throughout.
- **No placeholders:** every new symbol has a signature; SQL is concrete; the migration body is specified.
