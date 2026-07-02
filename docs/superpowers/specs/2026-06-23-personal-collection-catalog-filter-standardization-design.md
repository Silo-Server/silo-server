# Personal Collection Catalog Filter Standardization - Design

- **Date:** 2026-06-23
- **Status:** Draft (rev. 3 after adversarial review). Incorporates three findings: `match: any` merge safety, display-query fragment normalization, and ebook misclassification under unscoped watched filters.
- **Scope:** `silo-server` backend and web collection UI. Client follow-ups in `silo-android` and `silo-apple` are optional unless they adopt the same collection editing surface.
- **Note:** All paths are repository-relative. Commands assume the repository root is the cwd.

## Current Branch State

This design lives on the in-flight `profile-scoped-collection-filters` branch, which has **already implemented** the first cut of the feature but has **not merged to `main` and has not shipped to any client**. Concretely, on this branch today:

- A migration adds `watch_filter` and `media_filter` as `text` columns on `user_personal_collections`, each with a CHECK constraint (`migrations/sql/20260623184858_user_collection_display_filters.sql`).
- `internal/catalog/user_collection_display_filters.go` implements a custom Go filter pass (`FilterUserCollectionDisplayItems`) called from `internal/sections/fetcher.go` and `internal/catalog/catalog_resolver.go`.
- Web controls in `web/src/components/collections/CollectionBuilder.tsx`, `web/src/pages/ImportedCollectionEditor.tsx`, and helpers in `web/src/lib/collectionDisplayFilters.ts` already emit the legacy enums.

Because nothing is merged or released, this is a **clean-replacement** opportunity, not a backward-compatibility problem. The migration can be reshaped before merge; no client depends on the legacy enums. This resolves Open Question #1 below.

## Problem

Personal collection display filtering is growing its own filter model alongside two pre-existing completion implementations. There are **three** places that answer "is this item played/read for this profile?":

- the custom watched/media pass in `internal/catalog/user_collection_display_filters.go` (exact user-collection reads),
- the personalized `watched` query rule in `internal/catalog/query_builder.go` (`userStateCompletionClause`),
- the item user-data path in `internal/api/handlers/user_state.go` (`allEpisodesCompleted`), which is the canonical played/read state surfaced to clients.

That creates more than one way to answer the same product question: "should this item be visible for this profile based on watched state?" If multiple paths persist, they will drift in semantics, performance, and bug fixes. The drift is already visible *and already duplicated in code*:

- The custom personal collection helper treats series/seasons as watched only when all child episodes are complete; `userCollectionAllEpisodesCompleted` is a near-verbatim copy of `allEpisodesCompleted` in `user_state.go`.
- The catalog `watched` rule, by contrast, checks completion for the row's own `content_id` only (`uwp.media_item_id = mi.content_id`), so a series row is effectively never "watched" because progress/history is keyed to episode IDs, not the series ID.

Consolidating onto the catalog predicate therefore removes one Go copy outright and requires teaching the catalog predicate the rollup the other two already perform.

## Goals

- Use the catalog query system as the canonical representation and executor for watched filters.
- Make personal collection display filtering, catalog filtering, and collection-builder UI speak the same `QueryDefinition` / `QueryRule` language.
- Preserve exact collection source order when a collection is rendered as a shelf or source-ordered list.
- Preserve existing access controls and profile scoping.
- Avoid persisting a second watched-filter vocabulary unless needed as a compatibility shim.
- Keep the implementation narrow enough to land as a refactor/standardization of an already intended collection-filter feature.

## Non-Goals

- No new broad smart-collection DSL.
- No change to external sync membership. Imported lists still sync exact item membership before display filtering is applied.
- No change to collection ownership, sharing, or profile visibility rules.
- No client lockstep requirement. Existing clients can ignore new display-filter capabilities until they adopt the collection editor.
- No attempt to optimize very large exact collections beyond the current exact-source behavior in this pass.

## Scope Gate

`docs/architecture/v1-scope.md` says v1 is not locked and the proposal window is open. This design should be treated as a standardization of the profile-scoped collection filter work already underway, not as a separate feature expansion. If the final implementation exposes a new public capability beyond personal collection display filtering, file a v1 capability proposal before opening a feature PR.

## Design Principle

One feature gets one semantic implementation:

```json
{ "field": "watched", "op": "is", "value": true }
```

is the only canonical expression for "show watched items"; `value: false` is the canonical expression for "show unwatched items".

UI controls may present simpler labels such as "All", "Watched", and "Unwatched", but those controls must translate into catalog query rules before execution. Backend code should not add another watched evaluator for personal collections.

## Canonical Semantics

The canonical `watched` filter should match the user-facing played/read state shown by item user data:

| Item kind | `watched is true` should mean |
|---|---|
| Movie / episode / video leaf | Completed progress or visible completed history exists for the active profile. |
| Series | At least one child episode exists, and every child episode is completed for the active profile. |
| Season | At least one child episode exists, and every child episode in the season is completed for the active profile. |
| Ebook | Reader progress is at or above `models.EbookFinishedProgressThreshold`. |
| Manga | Use the same read-state source the catalog uses for manga/chapters once manga catalog filtering is finalized; do not add a collection-only interpretation. |

This intentionally aligns with `internal/api/handlers/user_state.go`, where series and season user data are derived by `allEpisodesCompleted`. If this definition is too expensive for catalog queries, we should optimize the catalog predicate rather than keep a separate personal collection definition.

For this table to hold when a query is *unscoped* (mixed-media exact collections), the predicate must pick the completion source from each row's own type, not from a single query-wide `media_scope` — see Catalog Watched Predicate Work. Picking the source by `media_scope` alone misclassifies any row whose type differs from the scope (notably ebooks under an unscoped query).

## Storage Model

Recommended canonical storage for personal collection display filters:

- Add a dedicated JSON column named for display filtering, `display_query_definition`. Do **not** overload the existing `query_definition` field (see Risks: it already carries `library_ids` for live/smart collections and is fragile enough to have needed a recent sanitization fix).
- Store the standard catalog `QueryDefinition` *shape* in that field, normalized to a filter-only fragment (see normalization rules below).
- For the current "media filter" control, store a **`type` rule**, not `media_scope`:
  - `all` -> no `type` rule
  - `movie` -> `{ "field": "type", "op": "is", "value": "movie" }`
  - `series` -> `{ "field": "type", "op": "is", "value": "series" }`
- For the current "watch filter" control, store a `watched` rule:
  - `all` -> no watched rule
  - `watched` -> `{ "field": "watched", "op": "is", "value": true }`
  - `unwatched` -> `{ "field": "watched", "op": "is", "value": false }`

**Why `type` rule, not `media_scope`:** `media_scope` is a single string on `QueryDefinition`, not a rule, and there is no logic to intersect two scopes (only `intersectCatalogDefinitionLibraries` exists, for library IDs). A `type` rule composes as an ordinary AND group and avoids any collision with a smart collection's own `media_scope`. `type` is already a supported, executable query field, so this needs no new merge machinery. Reserve `media_scope` for the source query's own scoping.

**`display_query_definition` is a filter-only fragment, not a full query.** A full `QueryDefinition` carries fields that are meaningless — and actively dangerous — for a display overlay. In particular the executor honors `def.Limit` internally, so a persisted display query with `limit` set would cap the match set *before* the exact-collection source-order reorder, silently and permanently hiding matching items and producing wrong totals/pages. `sort` and `library_ids` are likewise the source/collection's concern, not the overlay's. Normalize on the API boundary before persisting and before executing:

- Persist only `match` and `groups` (with rules drawn from the allowed display vocabulary — initially `type` and `watched`).
- Reject or strip `limit`, `sort`, `library_ids`, and `media_scope` from the fragment. Prefer reject-with-error on write so clients learn the contract; strip defensively on read for forward-compat.
- Storing the `QueryDefinition` *shape* keeps the door open to a richer rule set later; it does not mean accepting the whole `QueryDefinition` surface.

Compatibility: none required. The legacy `watch_filter` / `media_filter` enums exist only on this unmerged branch (see Current Branch State), so they are dropped rather than aliased. If — and only if — a future build ships the enums to clients before this lands, revisit the compatibility shim; until then there is no legacy surface to preserve.

## Execution Model

### Smart/live collections

Smart and live collections already resolve through `CatalogResolver.resolveQuerySource`. Their stored query is already a catalog `QueryDefinition`. Display filters should be merged into that query before execution.

**Merge must guarantee `(source) AND (display)` — appending a group does not.** `QueryBuilder.Build` joins *all* top-level groups with a single joiner derived from the definition's top-level `match` (`query_builder.go`: `topJoiner` is ` OR ` when `match == "any"`). So a smart/live source with `match: any` and groups `G1, G2` becomes `G1 OR G2`; appending a display group `D` yields `G1 OR G2 OR D` — which *widens* the result past the collection source and lets contradictory watched rules return rows instead of none. The QueryDefinition model has only one top-level `match` and only two levels (groups of rules), so there is no in-model way to express `(G1 OR G2) AND D`.

Merge rule:

- Keep library scope, `media_scope`, limit, and sort from the collection's source query.
- Combine the source definition and the display fragment with a true conjunction that is independent of either side's `match`. The robust implementation is an **executor-level conjunction**: build the source predicate and the display predicate as two separate SQL fragments and join them with ` AND ` (each side already parenthesized), rather than merging group lists. This is net-new merge logic, not an existing capability.
- The append-as-group shortcut is correct **only** when the source's top-level `match == "all"`. When `match == "any"` it is incorrect. Do not special-case "all" and forget "any"; implement the general conjunction and test both.
- Because the media control is stored as a `type` rule (not `media_scope`), it ANDs in through the same conjunction. If the source query's `media_scope` already excludes that type (e.g. a movies-only smart collection plus a `type is series` display filter), the result is empty — no scope-intersection code is needed. (There is no existing media-scope intersection logic to lean on, which is the main reason the media control is modeled as a `type` rule rather than a second `media_scope`.)
- If both the source and display layers contain `watched` rules, the conjunction preserves both; contradictory rules naturally return no rows.

### Exact/manual/imported collections

Exact collections have a source item set from `user_personal_collection_items`. Display filtering is applied after membership is known but before pagination.

**This path replaces two existing filters, not one.** Today the source-ordered exact path runs the in-Go `filterCatalogItems` (a metadata-only matcher that cannot evaluate personalized fields — it returns `false` for `watched`) *and* the separate `FilterUserCollectionDisplayItems` Go helper for the watched dimension. Neither is the SQL executor. Routing through the executor folds both metadata and personalized filtering into one SQL path; `FilterUserCollectionDisplayItems` is deleted and `filterCatalogItems` is no longer used for these collections.

Recommended execution:

1. Load collection item IDs in source order (from `user_personal_collection_items`, `ORDER BY position`).
2. Build a catalog `QueryDefinition` from the normalized display fragment plus any request overlay. Force `Limit = nil` and drop any fragment-level `sort`/`library_ids` so the executor cannot truncate or reorder the match set out from under step 4. (Defense in depth: the fragment is already normalized at persistence per the Storage Model, but the executor call must not reintroduce a limit.)
3. Run the normal catalog executor with `AccessFilter.AllowedContentIDs = itemIDs` so all filtering, including `watched`, uses the catalog query path. (`resolveCandidateItemsWithQuery` already establishes this pattern.)
4. For source-ordered requests, retrieve the *full* matching set (no limit), collect the matching IDs into a set, reapply the original source order, and only then apply request offset/limit. Request pagination is applied after reordering, never inside the executor.
5. For requests that sort via the query, allow the catalog executor to sort normally. **Note:** exact user collections are hardcoded to source order today (`UseSourceOrder: true`); this branch exists for forward-compatibility and is not exercised by current callers. Mark it clearly so it is not mistaken for present behavior.

This preserves exact collection order without duplicating watched logic. Step 4 is a new shape (run the executor for the match-set, then reorder), distinct from the sorted-pagination shape `resolveCandidateItemsWithQuery` uses today — call out the differing limit handling in the implementation.

## Catalog Watched Predicate Work

The existing catalog `watched` rule is already implemented but needs one semantic hardening before it becomes the shared path:

- Extract a reusable played/read predicate for `QueryBuilder`.
- Keep the existing leaf-item checks for progress, completed history, hidden history, and ebook reader progress.
- Add series and season rollup branches so `watched` matches item user-data semantics.
- Add SQL tests that assert series/season predicates are present only when relevant.
- Reuse the same predicate for `last_watched` only if the semantics truly match; otherwise leave `last_watched` as a leaf/history timestamp filter and document the difference. (`last_watched` is a timestamp aggregate built from the `user_last_watched` CTE — different machinery from the completion predicate — so it most likely stays separate.)

**Episode-set parity is the load-bearing requirement, not the rollup branch itself.** The Go rollups (`allEpisodesCompleted` in `user_state.go` and its copy in the collection helper) define "all episodes complete" over the episode set returned by the episode repository (`ListBySeriesIDs` / `ListBySeasonIDs`). If the SQL rollup's notion of "child episodes" differs — specials, unaired, or unavailable episodes included or excluded differently — the catalog `watched` rule will *disagree* with item user-data, recreating the exact drift this design removes, just relocated into SQL. The hardening must therefore reproduce the repository's child-episode set exactly (same joins, same filters), and a test must pin that equivalence (see Testing Strategy). Treat Open Question #4 (all-available vs all-aired) as a question about *what that repository set already is*, and match it.

**Row-type branching is mandatory for unscoped queries, including ebooks.** The current predicate selects the ebook source (`ebook_reader_progress`) *only* when `mediaScope == "ebook"`; otherwise it uses video watch progress/history (`userStateCompletionClause` branches on `qb.mediaScope == "ebook"`). Exact collection membership is not type-restricted, so an exact collection can contain an ebook while the query runs unscoped (the `all` media control emits no `type` rule). Under today's code that ebook would be evaluated against video progress/history — i.e. classified as never-watched regardless of read state — reintroducing exactly the kind of drift this design removes. The hardened predicate must therefore branch by the row's own type when the query is unscoped:

- movie / episode / video leaf -> watch progress + completed history,
- series / season -> episode rollup (with repository-parity episode sets, above),
- ebook -> `ebook_reader_progress >= EbookFinishedProgressThreshold`,
- manga -> deferred (see Non-Goals / Canonical Semantics).

If row-type branching cannot land in the first cut, v1 must instead *enforce* a video-only scope at execution for exact collections (so ebooks are never silently classified by the video source) and say so explicitly — but row-type branching is the correct end state because it is what makes the canonical semantics table hold under unscoped queries.

The first implementation may still gate series/season rollup behavior by `mediaScope == "series"` and exact known season rows, but it must not assume a single item kind for unscoped/video queries that can return movies, series, and ebooks together.

## API Shape

Preferred API model:

- Add `display_query_definition?: QueryDefinition` to personal collection create/update/response types.
- Replace the branch's `watch_filter` / `media_filter` request and response fields with `display_query_definition`. Since the enums are unreleased (see Current Branch State), no aliasing or deprecation header flow is required.
- Keep collection capabilities additive:
  - expose supported display-query fields using the existing catalog field vocabulary,
  - optionally expose UI presets for "all/watched/unwatched" and "all/movies/series" as client convenience.

Do not add a separate `filter_watched` query parameter to catalog endpoints. Catalog already has the watched rule.

## UI Model

The web UI can keep simple controls:

- media segmented control: All / Movies / Series,
- watched segmented control: All / Unwatched / Watched.

Those controls should edit a `QueryDefinition` fragment using shared helpers, not a separate collection-only state model.

Recommended helper location:

- `web/src/lib/collectionDisplayFilters.ts` should convert between UI presets and catalog `QueryDefinition`.
- `web/src/components/collections/CollectionGuidedRulesEditor.tsx` remains the broader catalog-query editor.
- `web/src/components/collections/CollectionBuilder.tsx` and `web/src/pages/ImportedCollectionEditor.tsx` use the shared helper.

## Migration Strategy

The legacy `watch_filter` / `media_filter` enums exist only on this unmerged branch and have shipped to no client (see Current Branch State), so the clean-replacement path applies. There is no backfill, dual-read, or deprecation window:

- Reshape the existing migration `20260623184858_user_collection_display_filters.sql` rather than layering a second migration on top: drop the `watch_filter` / `media_filter` columns and their CHECK constraints, and add the canonical `display_query_definition jsonb` column (nullable; `NULL` / absent means "no display filter"). Because the branch is unmerged, editing the not-yet-applied migration in place is cleaner than a forward-then-backfill sequence.
- Update API and web types to use display-query-backed helpers; delete the legacy enum request/response fields.
- No server code reads or writes the legacy columns after this change.

A backfill/dual-read migration would only be warranted if a build ships the legacy enums to clients before this lands. If that happens, fall back to: add `display_query_definition`, backfill (`watch_filter = 'watched'` -> watched rule true; `'unwatched'` -> false; `media_filter = 'movie'`/`'series'` -> `type` rule; `all` omits the rule), read legacy as fallback, stop writing legacy, and remove later under the API deprecation flow. Treat this as a contingency, not the plan of record.

## Risks

- **Merge OR-leak (smart/live):** appending a display group to a `match: any` source query widens results past the collection source instead of constraining them. Mitigate with the executor-level conjunction (Execution Model) and a test that a `match: any` source plus a display filter never returns rows the source alone would exclude.
- **Display fragment truncation:** a persisted `limit` (or stray `sort`) on `display_query_definition` would cap or reorder the exact-collection match set before the source-order reorder, hiding items and corrupting totals. Mitigate by normalizing the fragment to filter-only on write and forcing `Limit = nil` on the match-set execution.
- **Ebook misclassification under unscoped queries:** an ebook in a mixed-media exact collection evaluated by an unscoped `watched` rule is classified by the video source today (`media_scope`-gated ebook predicate). Mitigate with row-type branching in the predicate, or enforce a video-only execution scope for v1 (Catalog Watched Predicate Work).
- **Episode-set divergence (highest risk):** if the SQL rollup's child-episode set differs from the repository's `ListBySeriesIDs` / `ListBySeasonIDs` listing, the catalog `watched` rule will disagree with item user-data and reintroduce drift in a new place. Mitigate by reproducing the repository's joins/filters exactly and pinning the equivalence with a test. This, not raw SQL cost, is the thing most likely to ship subtly wrong.
- **Series/season SQL cost:** all-episodes-complete checks are more expensive than leaf checks. Mitigate by only emitting rollup branches when item types can include series/seasons and by relying on indexes on episode parent IDs and progress/history media IDs.
- **Semantics change for catalog series filters:** extending catalog `watched` to match displayed played state can change existing catalog results for series (today they are effectively never "watched"). This is likely correct, but it must be called out in release notes or PR text.
- **Exact collection performance:** source-order rendering requires evaluating the full exact collection before pagination (the executor must return the complete match-set to reorder). This matches current behavior but should not regress further; very large exact collections remain out of scope this pass (see Non-Goals).
- **Field overloading (resolved → dedicated column):** code inspection confirms `query_definition` already carries `library_ids` for live/smart collections and recently needed a `library_ids` sanitization fix. Reusing it for display filters would entangle two concerns; use a dedicated `display_query_definition` column.

## Testing Strategy

Backend tests are justified because this changes shared personalized filtering semantics:

- **Equivalence test (highest value):** for the same series/season/profile fixtures, assert the catalog `watched` rule and `user_state.go`'s `Played` flag agree. This is the "one semantic" goal made executable and is the test most likely to catch episode-set divergence; it should fail loudly if the two definitions drift.
- `internal/catalog/query_builder_test.go`: `watched` emits leaf completion checks and series/season rollup checks as appropriate.
- `internal/catalog/query_executor_test.go` or focused resolver tests: exact collection filtering routes through catalog `watched` and preserves source order; the executor returns the full match-set so reordering is stable across pages.
- `internal/catalog/catalog_resolver_test.go`: contradictory display/source filters return no rows; a `type is series` display filter on a movies-only smart collection yields no rows without any scope-intersection code.
- **Merge safety:** a smart/live source with top-level `match: any` plus a display filter must never return rows the source alone excludes (guards against the OR-leak); a contradictory `watched` overlay on a `match: any` source returns zero rows.
- **Fragment normalization:** a `display_query_definition` carrying `limit`/`sort`/`library_ids` is rejected on write (or stripped on read); an exact collection larger than any plausible page returns the full match set in source order regardless of fragment contents.
- **Ebook under unscoped watched:** a mixed-media exact collection (movie + ebook) with an `unwatched`/`watched` filter classifies the ebook via `ebook_reader_progress`, not video history (guards against the misclassification finding).
- `internal/api/handlers/collections_test.go`: create/update round-trips `display_query_definition`; the removed `watch_filter` / `media_filter` fields are gone from request and response.
- Migration test only if the contingency backfill path is used (it is not, on the plan of record).

Frontend tests should stay targeted:

- helper tests for converting simple UI controls to/from `QueryDefinition`,
- one editor test proving the submit payload carries the canonical display query.

## Implementation Outline

1. Harden catalog `watched` semantics (series/season rollup with repository-parity episode sets) and land the equivalence test. This is independently valuable and reviewable; consider shipping it as its own PR first.
2. Reshape migration `20260623184858` to drop `watch_filter` / `media_filter` and add `display_query_definition jsonb`.
3. Add display-query types and normalization helpers in backend collection handling.
4. Replace both the custom `FilterUserCollectionDisplayItems` pass and the in-Go `filterCatalogItems` source-order path with catalog-query execution constrained by exact item IDs (match-set, then reorder).
5. Update web controls to emit catalog query fragments through shared helpers; delete the legacy enum fields.
6. Run focused Go tests for catalog/query/user-collection paths and targeted frontend helper tests.

Per the v1 process, link the capability sub-issue (`Part of #NNN`) and keep PRs to one concern — predicate-hardening (step 1) is a natural standalone PR separate from the storage/UI reshape (steps 2–5).

## Open Questions

- **Resolved.** The legacy enums exist only on this unmerged, unreleased branch, so it replaces them cleanly with no compatibility shim (see Current Branch State and Migration Strategy).
- **Decided:** the standardized *controls* expose video item scopes (movie/series/episode) for v1; manga read-state filtering is deferred until catalog manga filtering is finalized, then adopts the same catalog read-state source rather than a collection-only interpretation. Note this constrains the *control vocabulary*, not collection *membership* — exact collections can still contain ebooks, so the watched predicate must branch by row type (see Catalog Watched Predicate Work) regardless of what the control offers.
- **Decided:** first version exposes only the `type` and `watched` controls, stored as a **filter-only `QueryDefinition` fragment** (`match` + `groups`; no `limit`/`sort`/`library_ids`/`media_scope`). Keeping the fragment shape lets a richer rule set turn on later without a storage change, while normalization prevents the fragment from carrying execution-controlling fields (see Storage Model).
- Should catalog `watched` rollup for series mean "all available episodes complete" or "all aired episodes complete"? This is a question about *what the episode repository already returns* — match that set exactly (see Catalog Watched Predicate Work / episode-set parity). Only change the definition by changing the repository path and item user-data together, never the catalog rule alone.
- **New:** confirm no `silo-android` / `silo-apple` build has already consumed the branch's `watch_filter` / `media_filter` fields. The design assumes not (the branch is unreleased); if a client has pinned them, the contingency backfill path applies instead.
