# Server-side typeahead for catalog facets

**Status:** Draft. Architecture sketch; no implementation yet.

## Why

`b95494c` capped `/api/v1/catalog/filters` facet responses to 1000
distinct values each, which fixed an 11.7 MB payload on the audiobook
library (88k authors, 92k narrators, 161k series). The cap is a
stopgap — a user looking for an author past the first 1000
alphabetically can't find them through the dropdown.

Parallel context: a separate investigation (transcript in the
2026-05-27 sessions) compared how `librarymanagerre`, `booklore-ng`,
and `audiobookshelf` paginate large libraries. The cursor +
virtual-scroll camp (librarymanagerre, booklore-ng's
`@tanstack/react-virtual`) scales to 250k+ items by never sending the
client a full list; the eager-shelves camp (audiobookshelf web +
mobile) renders one DOM div per item and falls over above ~30–50k.
The 11.7 MB facet issue here is the same shape on a different surface
— "too much data shipped to the client at once."

The cleanest fix is **server-side typeahead**: the client sends a
search prefix as the user types, the server returns the top N matches
for that prefix.

Commands assume the repository root is the cwd.

## Surface

### `/api/v1/catalog/filters/search`

New endpoint, scoped per facet, query parameters mirror
`/api/v1/catalog/filters` (`source`, `library_id`, etc.) plus:

- `facet=author|narrator|series|studio|network|country|genre|original_language|content_rating`
- `q=<prefix>` — the user's typed prefix (1-64 chars, trimmed)
- `limit=<N>` — capped at e.g. 50

Response:

```json
{ "matches": ["Brandon Sanderson", "Brandon Sanderson & Steven Erikson", ...], "has_more": false }
```

Returns up to `limit` matches sorted by:
1. exact-prefix match first (`q` matches the start of the value)
2. then case-insensitive substring match
3. then alphabetical

`has_more` is true when the underlying result set was truncated.

### `/api/v1/catalog/filters` keeps the current shape

No change to the existing endpoint — it still returns the capped top
1000 per facet for the initial dropdown render. The typeahead surface
takes over once the user starts typing.

## Backend changes

### `internal/catalog/catalog_resolver.go`

New method `SearchFacetWithOptions(ctx, req, access, facet, prefix,
limit)` that:
1. Resolves the same access/scope as `ListFiltersWithOptions`.
2. Dispatches to a facet-specific helper based on `facet`.
3. Returns the matches + has_more flag.

Add to `facetFetcher` interface:

```go
SearchPeopleByKind(ctx, kind models.PersonKind, filters BrowseFilters, baseRelation, mediaScope, prefix string, limit int) ([]string, bool, error)
SearchAudiobookSeries(ctx, filters BrowseFilters, baseRelation, mediaScope, prefix string, limit int) ([]string, bool, error)
SearchDistinctArrayColumn(ctx, column string, filters BrowseFilters, baseRelation, mediaScope, prefix string, limit int) ([]string, bool, error)
SearchDistinctScalarColumn(ctx, column string, filters BrowseFilters, baseRelation, mediaScope, prefix string, limit int) ([]string, bool, error)
```

Each helper runs the existing facet SQL with an added
`WHERE LOWER(<value>) LIKE LOWER($N || '%')` (prefix match) and
`LIMIT N+1` (so we can detect has_more by checking if the result set
exceeds N).

### `internal/api/handlers/catalog.go`

New handler `HandleCatalogFacetSearch` mounted at
`/api/v1/catalog/filters/search`.

## Frontend changes

### `web/src/components/ui/searchable-select.tsx` (or a new variant)

`SearchableSelect` today does client-side filtering over the full
`options` array. Replace it for the high-cardinality facets with a
debounced server search:

- Type `<= 100ms` of inactivity → fire `/catalog/filters/search?facet=author&q=...`
- Reset focus + cancel in-flight when the user keeps typing
- Show the first 50 matches; "show more" disabled (the user types more
  to narrow further)

The existing `SearchableSelect` stays for low-cardinality facets
(genres, content_ratings, etc.) where the initial 1000 is plenty.

### `web/src/components/collections/CollectionGuidedRulesEditor.tsx`

Author / Narrator / Series sections switch to the new typeahead-backed
select. Studio / Network / Country can stay on `SearchableSelect`
(low-cardinality) or migrate later for consistency.

## Tests

- Backend: search SQL emits the LIKE prefix + LIMIT N+1, has_more
  semantics, access filter still gates the result set.
- Frontend: typeahead component debounces, cancels stale requests,
  renders empty / loading / no-results states.

## Migration

None — additive endpoint. Frontend can be rolled out incrementally:
audiobook-only sections first (since they're the worst offenders),
others later.

## What this plan does not cover

- The audiobookshelf-app pagination question from the separate
  transcript. That's a different surface (the ABS-compat
  `/abs/api/libraries/{id}/items` endpoint on `:13378`), not the silo
  native catalog. Same architectural family but the patch lives in
  `internal/audiobooks/abs/libraries_handler.go`, not catalog.
- A general client-side virtual scroll for the library grid itself.
  That's the cousin problem ("250k books rendered as 125k shelf
  divs"); cross-applies but is its own work.

## Risk / rollout

- New endpoint is opt-in for the frontend — existing callers keep
  using the bulk `/filters` response.
- Typeahead latency budget: each keystroke fires one DB query against
  the same indexes the bulk facets already use. Should be fast
  (`item_people(content_id, kind)` covers people lookups,
  `audiobook_series(content_id)` covers series, and a `LOWER()`
  functional index already exists on
  `audiobook_series_name_lower`). Verify with EXPLAIN before shipping.
