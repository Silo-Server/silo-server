# Personal Collection Catalog Filter Standardization - Implementation Plan

- **Date:** 2026-06-23
- **Spec:** `docs/superpowers/specs/2026-06-23-personal-collection-catalog-filter-standardization-design.md` (rev. 3, post-adversarial-review). The spec owns the *why* and the decisions; this plan owns the *how*, with concrete files and ordering.
- **Note:** All paths are repository-relative. Commands assume the repository root is the cwd.

## Approach & PR Boundaries

Three concerns, landed in order. PR 1 is independently valuable and must merge first because PR 2 depends on its hardened predicate for correctness.

| PR | Concern | Depends on |
|----|---------|-----------|
| **PR 1** | Harden the catalog `watched` predicate (series/season rollup + row-type branching) and pin equivalence with item user-data. | — |
| **PR 2** | Replace the `watch_filter` / `media_filter` enum vocabulary with a `display_query_definition` fragment end to end (migration → storage → API → execution → web). | PR 1 |
| **PR 3** (deferred) | Wire display filters for smart/live collections via an executor-level `(source) AND (display)` conjunction. | PR 2 |

**Why PR 3 is deferred:** display-filter controls are currently exposed only for manual/exact and imported collections (`CollectionBuilder.tsx` renders them only when `collection_type === "manual"`; imported uses `ImportedCollectionEditor.tsx`). Smart collections never reach `FilterUserCollectionDisplayItems` today (`catalog_resolver.go` branches smart collections to `resolveQuerySource` before the filter call). So the OR-leak the adversarial review found is a guardrail for *future* wiring, not a v1 path. Build PR 2 so the same conjunction helper is reusable, but do not expose smart-collection display filters until PR 3.

---

## PR 1 — Catalog Watched Predicate Hardening

**Goal:** make the catalog `watched` rule produce the same played/read verdict as item user-data (`internal/api/handlers/user_state.go`) for every item kind, under both scoped and unscoped queries. This is the single-semantic foundation; everything else routes through it.

### Current state (verified)

- `internal/catalog/query_builder.go`:
  - `buildWatchedClause` (≈900–912) wraps `userStateCompletionClause` and negates for `value:false`.
  - `userStateCompletionClause` (≈1119–1171) checks completion against **the row's own `content_id`** (`uwp.media_item_id = mi.content_id` / `uwh.media_item_id = mi.content_id`). No episode rollup → a series row is effectively never watched.
  - `ebookUserStateCompletionClause` (≈1173–1193) is selected **only when `qb.mediaScope == "ebook"`** → an ebook under an unscoped query is misclassified by the video clause.
- `internal/api/handlers/user_state.go` `allEpisodesCompleted` (≈171–185) defines the canonical rollup over the episode repository's set.
- Episode set (the parity target): `internal/catalog/episode_repo.go` `ListBySeriesIDs` / `ListBySeasonIDs` select from `episodes` filtered by `series_id` / `season_id` **AND** `episodeAvailabilityPredicate` = `EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id = episodes.content_id)`. No specials/season-0 exclusion, no media-file requirement. Links: `episodes.series_id`, `episodes.season_id`.
- The base relation always exposes `mi.type` (`media_items mi`, or the episode derived table that hard-codes `'episode'::text AS type`), so a `CASE` on row type is feasible.

### Changes

1. **Extract a reusable completion predicate.** In `internal/catalog/query_builder.go`, refactor `userStateCompletionClause` into a helper that, given a row alias and the linking column, emits the leaf completion EXISTS pair (progress + history, with hidden-history guard). Keep `ebookUserStateCompletionClause` as the ebook leaf. This is the shared building block for the branches below.

2. **Add series/season rollup branches with repository-parity episode sets.** Emit, for a series row, "≥1 child episode exists AND no child episode is incomplete," expressed so the child-episode set is **identical** to `episodeAvailabilityPredicate`:
   ```sql
   EXISTS (SELECT 1 FROM episodes e
           WHERE e.series_id = mi.content_id
             AND EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id = e.content_id))
   AND NOT EXISTS (
     SELECT 1 FROM episodes e
     WHERE e.series_id = mi.content_id
       AND EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id = e.content_id)
       AND NOT (<leaf completion predicate for e.content_id>))
   ```
   Season rollup is the same with `e.season_id = mi.content_id`. The leaf predicate inside the `NOT EXISTS` reuses the helper from step 1 keyed on `e.content_id`. **The `episode_libraries` EXISTS must match `episodeAvailabilityPredicate` verbatim** — if `episode_repo.go` ever changes that predicate, this SQL changes with it. Add a code comment cross-referencing `episodeAvailabilityPredicate` so the coupling is discoverable.

3. **Branch by row type for unscoped queries.** When `qb.mediaScope` is empty/`video` (rows can be mixed), select the completion source from `mi.type` rather than from `mediaScope`:
   - `movie` / `episode` / video leaf → leaf predicate,
   - `series` → series rollup, `season` → season rollup,
   - `ebook` → ebook leaf,
   - `manga` → deferred; treat as never-completed for now (documented), do not invent a collection-only interpretation.
   When `mediaScope` is a single concrete kind (e.g. `series`, `ebook`), the optimizer-friendly path may emit just that branch. Implement as a `CASE mi.type WHEN ... END` or chained `(mi.type = 'x' AND <clause>) OR ...`; verify argIdx accounting (each leaf/ebook clause increments `qb.argIdx` by 2 — the rollups reuse the leaf, so count the placeholders carefully and add a builder test that the produced arg count matches bound args).

4. **`last_watched` stays separate.** Do not route `last_watched` through the new predicate; it is a `user_last_watched` CTE timestamp aggregate. Leave as-is; add a one-line comment noting the intentional divergence.

### Tests (PR 1)

- `internal/catalog/query_builder_test.go`: assert the `watched` clause contains the series/season rollup EXISTS only when the scope/row-type can include series/seasons; assert arg count == bound args for each branch; assert the `episode_libraries` predicate text is present in the rollup.
- **Equivalence test (highest value):** seed a Postgres fixture with a series whose episodes are variously completed/not, a season, a movie, and an ebook for one profile; assert the catalog `watched` rule's matched set equals exactly the items whose `user_state.go` `Played` flag is true. Because `user_state.go` lives in `internal/api/handlers` (CGO/libvips), run this in the libvips-capable container; the test can drive both the catalog executor and the item-user-state builder against the same pool and diff the verdicts. If a same-package home is awkward, place the comparison test in `internal/api/handlers` where both sides are reachable.

### Release note (PR 1)

Catalog `watched` filtering for series/seasons changes from "never matches" to "matches when all available episodes are completed." Call this out in the PR body and release notes per the spec's semantics-change risk.

---

## PR 2 — Replace the Enum Vocabulary with `display_query_definition`

One concern: swap the persisted representation and every surface that reads/writes it. Clean replacement — no aliases, no backfill (the enums are branch-only and unreleased; see spec Current Branch State). Land the steps below together so no layer reads a column another layer has dropped.

### Step 2.1 — Migration reshape

Edit `migrations/sql/20260623184858_user_collection_display_filters.sql` in place so the canonical schema has no legacy columns:

- **Up:** `ALTER TABLE public.user_personal_collections ADD COLUMN IF NOT EXISTS display_query_definition jsonb;` (nullable; `NULL`/absent = no display filter). Remove the `watch_filter` / `media_filter` `ADD COLUMN` and their CHECK constraints.
- **Down:** `ALTER TABLE ... DROP COLUMN IF EXISTS display_query_definition;`

**Operational gotcha:** Goose keys applied migrations by version, so editing an already-applied migration will **not** re-run it. Any dev DB that already applied the original `20260623184858` (which added `watch_filter`/`media_filter`) must be rolled back to before it and re-applied: `make migrate-down` to the prior version, then `make migrate-up`. If a shared dev DB makes in-place editing risky, the alternative is a *new* timestamped migration (`make migrate-create NAME=replace_collection_display_filters`) that drops the two columns and adds `display_query_definition`; the spec prefers in-place reshape since nothing is merged, but call the choice out in the PR.

### Step 2.2 — Backend storage & types

- `internal/userstore/types.go`: in `Collection` (≈185–213), `CreateCollectionInput` (≈238–255), `UpdateCollectionInput` (≈257–278), remove `WatchFilter` / `MediaFilter`; add `DisplayQueryDefinition string` (and `*string` on the update input), mirroring the existing `QueryDefinition` fields.
- `internal/userstore/pgstore/collections.go`:
  - `collectionSelectColumns` (≈15–18): drop `watch_filter, media_filter`; add `display_query_definition`.
  - `scanCollection` (≈20–42) and the `ListCollections` scan (≈192–196): drop the two scan targets; add `&c.DisplayQueryDefinition`.
  - `CreateCollection` INSERT (≈79–94): drop the two columns/args; add `display_query_definition` + arg.
  - `UpdateCollection` (≈270–283): replace the two `add("watch_filter"…)` / `add("media_filter"…)` blocks with a single `display_query_definition` block driven by `input.DisplayQueryDefinition != nil`.
  - The normalization at create-time (≈59–68) moves from enum normalization to **fragment normalization** (Step 2.5).
- `internal/userstore/collection_filters.go`: repurpose into the fragment normalizer's home, or delete the enum constants/`Normalize*` funcs if nothing else uses them after the swap. Keep the file's tests meaningful — convert `collection_filters_test.go` to cover the new normalizer (Step 2.5) rather than the deleted enums.

### Step 2.3 — API surface

- `internal/api/handlers/collections.go`:
  - `createCollectionRequest` (≈39–50), `updateCollectionRequest` (≈52–67), `collectionResponse` (≈73–101): drop `watch_filter` / `media_filter`; add `display_query_definition json.RawMessage` following the existing `query_definition` handling (response assembly mirrors `defaultJSON([]byte(c.QueryDefinition))` at ≈843).
  - `HandleCreateCollection` (≈270–279) / `HandleUpdateCollection` (≈341–356): replace enum validation with fragment normalization/validation (Step 2.5); reject invalid fragments with `400`.
  - `collectionCapabilitiesResponse` (≈108–111) + `HandleCapabilities` (≈224–229): replace `watch_filters`/`media_filters` value lists with capability metadata describing the supported display-query fields (`type`, `watched`) and the UI presets (`all/watched/unwatched`, `all/movies/series`). Keep it additive and feature-detection-oriented per the v1 API rules.
- `internal/api/handlers/user_collection_imports.go`: `userImportSharedFields` (≈67–77) and `createImportedCollection` validation (≈214–223) — same swap to `display_query_definition`.

### Step 2.4 — Exact-collection execution through the catalog executor

Replace the custom Go filter pass and the metadata-only in-Go matcher with one SQL path.

- Delete `internal/catalog/user_collection_display_filters.go` (`FilterUserCollectionDisplayItems` and helpers) and its test, plus the duplicated `userCollectionAllEpisodesCompleted`.
- Call sites to rewrite:
  - `internal/catalog/catalog_resolver.go` `resolveUserCollectionSource` (≈479–526, filter call at ≈517).
  - `internal/sections/fetcher.go` `fetchUserCollection` (filter call at ≈1371).
- New behavior for exact collections (per spec Execution Model):
  1. Load member IDs in source order (`user_personal_collection_items`, `ORDER BY position`) — unchanged.
  2. Parse `collection.DisplayQueryDefinition` into a `QueryDefinition`, **force `Limit = nil`** and drop any fragment `sort`/`library_ids` defensively before execution.
  3. Run the executor with `AccessFilter.AllowedContentIDs = memberIDs` (the pattern `resolveCandidateItemsWithQuery` already uses at `catalog_resolver.go` ≈606–607). The executor's `conditions` slice AND-joins `AllowedContentIDs` with the display fragment, so membership ∧ filter is automatic — no OR-leak risk here.
  4. For source-ordered requests: request the **full** match set (no limit), collect matching IDs into a set, reapply original order, then apply request offset/limit. Pagination happens after reordering, never inside the executor.
  5. Stop using `filterCatalogItems` (`catalog_resolver.go` ≈1772) for these collections — replace the `UseSourceOrder` branch in `resolveExactOrderedMediaItems` (≈571–586) with the match-set-then-reorder path. **Do not delete `filterCatalogItems`:** it has a second caller at `catalog_resolver.go:244` (the query-source path), so it stays; only the exact-collection call at ≈575 changes.
- Empty display fragment ⇒ skip the executor filter entirely and return members in source order (preserves today's "no filter" performance for unfiltered collections).

### Step 2.5 — Fragment normalization (shared backend helper)

A `display_query_definition` is a **filter-only** `QueryDefinition` fragment. Add one normalizer (e.g. in `internal/catalog` next to `QueryDefinition`, or `internal/userstore`) used by every write path (create/update/import):

- Accept only `match` + `groups`, with rules limited to the allowed display vocabulary (v1: `type`, `watched`).
- Reject (preferred, on write) or strip `limit`, `sort`, `library_ids`, `media_scope`.
- Reject unknown fields/ops so clients learn the contract.
- Return a canonical JSON string for storage. Used by `pgstore` create/update and the API handlers.

### Step 2.6 — Web

The frontend already has the machinery: `QueryDefinition` types, `createEmptyQueryDefinition` / `normalizeQueryDefinition` (`web/src/api/types.ts` ≈1318–1361), and `CollectionGuidedRulesEditor.tsx` which already converts `watched` rules ↔ a `watchStatus` field (`queryDefinitionToGuidedState` ≈93–254, `guidedStateToQueryDefinition` ≈257–374). Reuse it; do not add a parallel state model.

- `web/src/lib/collectionDisplayFilters.ts`: replace the enum option lists / `normalize*` helpers with converters **preset ⇄ `QueryDefinition` fragment**:
  - `watched`/`unwatched`/`all` ⇄ a `{field:"watched",op:"is",value:true|false}` rule (or none),
  - `movie`/`series`/`all` ⇄ a `{field:"type",op:"is",value:"movie"|"series"}` rule (or none),
  - a builder that assembles the two presets into a single filter-only fragment, and a reader that derives the two preset values back from a fragment (for editing).
- `web/src/api/types.ts`: drop `UserCollectionWatchFilter` / `UserCollectionMediaFilter` and the `watch_filter` / `media_filter` fields on `Collection` (≈1270–1271), `CreateCollectionRequest` (≈1403–1404), `UpdateCollectionRequest` (≈1420–1421); add `display_query_definition?: QueryDefinition`.
- Editors — swap the two `<Select>` controls' wiring to read/write through the helper, keeping the same visible labels:
  - `web/src/components/collections/CollectionBuilder.tsx` (value shape ≈45–57, UI ≈259–313, submit ≈88–101),
  - `web/src/pages/ImportedCollectionEditor.tsx` (state ≈118–125, UI ≈277–354, save ≈184–214),
  - `web/src/components/CollectionTemplateGallery/UserCollectionTemplateConfigForm.tsx` (state ≈99–100, submit ≈119–167).
- `web/src/hooks/queries/collections.ts` (`useCreateCollection` ≈85–101, `useUpdateCollection` ≈103–127): payloads now carry `display_query_definition` instead of the enums (the generic `buildUserCollectionPayload` needs no change beyond the type swap).

### Tests (PR 2)

- `internal/api/handlers/collections_test.go`: create/update/get round-trips `display_query_definition`; `watch_filter`/`media_filter` are absent from request and response; an invalid fragment (e.g. containing `limit` or an unknown field) returns `400`.
- Resolver/executor tests:
  - exact collection routes through the catalog `watched` rule and preserves source order across pages;
  - an exact collection larger than a page returns the full match set in source order regardless of fragment contents (guards the limit-truncation finding);
  - a mixed-media exact collection (movie + ebook) with `watched`/`unwatched` classifies the ebook via `ebook_reader_progress`, not video history (relies on PR 1 row-type branching).
- Fragment-normalizer unit tests (replacing `collection_filters_test.go`): strips/rejects `limit`/`sort`/`library_ids`/`media_scope`; round-trips `type` + `watched` rules.
- Frontend: helper test for preset ⇄ fragment conversion (both directions); one editor test asserting the submit payload carries the canonical `display_query_definition`.

---

## PR 3 — Smart/Live Display Filters (deferred)

Only when smart/live collections expose display-filter controls. Implements the spec's executor-level conjunction so `(source) AND (display)` holds regardless of the source's top-level `match`.

- Seam: `internal/catalog/query_executor.go`, after the `AllowedContentIDs` handling (≈322) and before `ApplySectionAccessFilter`. Build the display fragment with a second `NewQueryBuilder("mi").WithArgIdx(argIdx)`, `rebindSQLPlaceholders` its output to the running `argIdx`, append the rebound clause to `conditions`, and advance `argIdx`/`args`. Because `conditions` is AND-joined (≈353), this yields `(sourceWhere) AND (displayWhere)` with no group-list manipulation and no OR-leak.
- Do **not** append the display group into `def.Groups` — that is the OR-leak the adversarial review found (`QueryBuilder.Build` joins top-level groups with the def's single `match`, ≈84–111).
- Tests: a `match: any` source plus a display filter never returns rows the source alone excludes; a contradictory `watched` overlay on a `match: any` source returns zero rows.

---

## Verification (every PR, pre-push)

Per `CLAUDE.md`:

- `make lint`
- `cd web && pnpm run lint && pnpm run format:check`
- `make verify-local-paths`
- Go tests in a **libvips-capable container** (a bare-host `go test ./...` silently skips CGO packages including `internal/api/handlers`, where the equivalence and DTO tests live). Prefix `GOWORK=off` for builds/tests in this repo.
- Targeted: `go test ./internal/catalog/... ./internal/api/handlers/... ./internal/userstore/...` and the relevant `web` helper/editor tests.

## Sequencing summary

1. PR 1 (predicate + equivalence test) → merge.
2. PR 2 (migration → types/storage → API/imports → fragment normalizer → exact execution → web), landed together → merge.
3. PR 3 only if/when smart-collection display filters are wired.

Link each PR to the capability sub-issue (`Part of #NNN`) per the v1 process; keep one concern per PR.

## Open items to confirm before/while implementing

- Confirm no `silo-android` / `silo-apple` build already consumes the branch's `watch_filter` / `media_filter` fields (spec assumes unreleased; if pinned, fall back to the contingency backfill path in the spec's Migration Strategy).
- Confirm `episodeAvailabilityPredicate` is the intended "child episode" definition to match (Open Question #4 in the spec) — it currently includes specials/season-0 if they have library links. Match it exactly; change item user-data and the catalog rule together if it should differ.
- Decide the fragment normalizer's package home (`internal/catalog` vs `internal/userstore`) based on import direction — it needs the `QueryDefinition` type without creating an import cycle.
