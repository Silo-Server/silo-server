# Audiobook-native catalog filter fields

**Status:** Draft — not yet approved or executed.

**Context.** The audiobooks library page currently inherits the catalog's
movie/TV filter set. We've already (a) wired `libraryType="audiobook[s]"`
into the sort-relevance scope so video-only sorts (resolution, IMDb/RT
ratings, content rating) drop out, and (b) gated the obviously-irrelevant
video-only filter sections (Director, Writer, Producer, Studio, Network,
Video Quality) in `CollectionGuidedRulesEditor`. What remains is
**affirmative** support for audiobook-native filter dimensions: **author**,
**narrator**, **series**.

Commands assume the repository root is the cwd.

## Goal

Surface three new filter dimensions on the audiobook library page:

1. **Author** — `item_people.kind = PersonKindAuthor` (7).
2. **Narrator** — `item_people.kind = PersonKindNarrator` (8).
3. **Series** — `audiobook_series.series_name` (free-text or distinct list).

The user should be able to:
- See per-dimension counts/options in the filter sheet (populated from the
  current scope, like Genres/Studios already do).
- Add a rule via the guided editor that filters items by exact author /
  narrator / series.

## Non-goals

- Series **ordering** (we already have `audiobook_series.series_index`).
  Sort by series is a separate piece of work; this plan focuses on
  filtering only.
- Author/narrator typeahead UI improvements beyond the existing
  `PersonSearchSelect` (reuse it with kind-scoped lookups).

## Backend

### `internal/catalog/catalog_resolver.go`

`CatalogFiltersResult` currently has `Genres`, `Studios`, `Networks`,
`Countries`, `OriginalLanguages`, `ContentRatings`, `Resolutions`,
`AudioLanguages`, `SubtitleLanguages`. Add:

```go
Authors   []string `json:"authors,omitempty"`
Narrators []string `json:"narrators,omitempty"`
Series    []string `json:"series,omitempty"`
```

In `listFiltersForSource`, add three parallel facet queries:

- `listDistinctPeopleByKind(ctx, scope, models.PersonKindAuthor)` — joins
  `item_people` + `people` on the result set defined by the current
  request scope (library_ids, media_scope=audiobook, etc.) and returns
  distinct `people.name`.
- Same for `PersonKindNarrator`.
- `listDistinctAudiobookSeriesNames(ctx, scope)` — distinct
  `audiobook_series.series_name` for the scope, joined via
  `audiobook_series.content_id = media_items.content_id`.

All three need to respect the access filter the caller passes in (mirror
how Genres/Studios already gate by access).

### `internal/api/handlers/catalog.go`

Extend `catalogFiltersResponse` and `HandleGetCatalogFilters` to emit the
new fields. Mirror the pattern used for the existing ones — no flag
gating, audiobook filters should always be included when present.

### `internal/catalog/query_builder.go`

Three new field names: `author`, `narrator`, `series`. The first two
reuse `buildPersonClause` with the appropriate `PersonKind`. `series`
needs a small new clause:

```go
case "series":
    // EXISTS (SELECT 1 FROM audiobook_series s
    //         WHERE s.content_id = media_items.content_id
    //           AND lower(s.series_name) = lower($N))
```

Register the three field names in `catalogQueryRuleFields` so the parser
accepts them. None of them are personalized — they don't go into
`catalogPersonalRuleFields`.

### Tests

- `internal/catalog/catalog_resolver_test.go` — extend with audiobook
  scope: assert authors/narrators/series come back, scoped to libraries
  the test user has access to.
- `internal/catalog/query_builder_test.go` — add cases for `author`,
  `narrator`, `series` rules; verify the emitted SQL clauses match the
  expected shape.

## Frontend

### `web/src/api/types.ts`

Extend `CatalogFiltersResponse` (and any `ItemFiltersResponse` base, if
shared) with optional `authors?: string[]`, `narrators?: string[]`,
`series?: string[]`.

Extend `QueryRule['field']` to include `'author' | 'narrator' | 'series'`
if it's a literal union (or no-op if it's `string`).

### `web/src/components/collections/CollectionGuidedRulesEditor.tsx`

Add to `GuidedFormState`:
- `author: string`
- `narrator: string`
- `series: string`

Update `queryDefinitionToGuidedState` / `guidedStateToQueryDefinition` to
round-trip the three rules (mirror how `actor` / `director` are wired).

In the audiobook-only render path (already gated via `isAudiobookLibrary`),
add a new section *above* the country row:

```tsx
<div className="grid gap-4 md:grid-cols-2">
  <Author picker />        {/* PersonSearchSelect with kind="author" */}
  <Narrator picker />      {/* PersonSearchSelect with kind="narrator" */}
</div>
<div className="grid gap-4 md:grid-cols-2">
  <Series picker />        {/* SearchableSelect over filters.series */}
</div>
```

`PersonSearchSelect` likely needs a new `kind` prop so it can scope its
backend lookup to authors or narrators. If today it queries all kinds,
we'll add a query param + handler-side filter.

### `ActiveFilterBadges` + `catalogFilterBadges`

Add badge rendering for the three new fields so they appear in the
selected-filters chip row above the editor.

### Tests

- `web/src/components/collections/CollectionGuidedRulesEditor.test.tsx`
  — render with `libraryType="audiobooks"` and a filters payload
  containing authors/narrators/series; assert all three sections appear
  and update the query definition correctly.

## Migration / data

No schema changes required. `PersonKindAuthor=7` and `PersonKindNarrator=8`
already exist in `internal/models/media.go`. `audiobook_series` already
holds series names (migration 145). The scanner is already writing both.

## Risk

- **Filter facet performance.** Three new distinct-value queries per
  `/catalog/filters` request. The existing facet queries use the same
  `facetFetcher` infrastructure, so they'll inherit the same query-time
  budget. For an audiobook library on the order of a few thousand items
  this should be fine; if it's not, we can cache author/narrator lookups
  more aggressively (they change rarely).
- **PersonSearchSelect kind scoping.** If the component today emits a
  single combined kind=any search, we need to add kind-filtered variants
  without breaking the existing actor/director/writer/producer pickers.
- **Series name normalization.** Backfill at migration 145 used regex
  parsing; some items may have inconsistent casing or whitespace.
  Distinct facet query should `TRIM(series_name)` and present `LOWER`
  for matching; otherwise duplicates show up in the picker.

## Rollout

This is gated entirely behind `libraryType === "audiobook[s]"` on the
frontend, and the new filter fields are additive on the backend. No
feature flag needed. Ship in two PRs if reviewer prefers:

1. **Backend** — filter response fields + query-builder clauses + tests.
2. **Frontend** — types + GuidedFormState + editor sections + tests.

Or a single PR if the diff stays reviewable (~600 lines including tests).

## Verification

- Add new tests as above; `make lint` and `cd web && pnpm vitest run`
  should pass.
- Manually: open the audiobook library filter sheet, pick an author and
  a narrator, confirm the result set narrows. Pick a series, confirm
  same.
