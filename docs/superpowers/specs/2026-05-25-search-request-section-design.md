# Search Request Section Design

## Goal

Surface TMDB-backed "requestable" results inside the main catalog search so users can discover and request items that aren't in their library without leaving the search flow. The library remains the primary surface; requestable results are an additive, clearly delimited section that never blocks or displaces library results.

## Behavior

### Layout (both surfaces)

- The library section renders first using existing FTS results. No changes to library ranking, pagination, or row layout.
- A "Request to Add" section renders below the library results when:
  - admin `RequestsEnabled = true`, AND
  - the viewer has a profile, AND
  - the TMDB query returns at least one result that is not already in the library.
- The Cmd+K search dialog (`GlobalSearch`) shows up to 4 TMDB rows beneath a single "Not in your library?" CTA strip.
- The full search results page (`Catalog`) shows a section divider, a section header, then a grid of up to 20 TMDB cards on initial render.
- Clicking any TMDB row or card navigates to the existing `/requests/{media_type}/{tmdb_id}` detail page. The detail page is responsible for the actual request action and confirmation.

### Section header copy

- When library has ≥1 hit: header reads "Request to Add".
- When library has 0 hits and TMDB has ≥1 hit: header is replaced by a soft framing — "Not in your library, but you can request" — and there is no separate empty state for library.
- When both sources return 0 results: the existing "No matches" / "No items found" empty state is unchanged; no requestable section renders.

### Quota / blocked viewers

- If the viewer is quota-exhausted or individually blocked, the section still renders, but each row's request affordance is disabled with a tooltip explaining the reason. Rows remain clickable and still navigate to the detail page.

### Performance

- Library results never wait on TMDB. The two queries fire concurrently from the client; the library section paints as soon as FTS returns.
- TMDB query is debounced at 400ms; library query stays at the current 200ms.
- TMDB query is cancelled in-flight when the query string changes, using the existing react-query `{ signal }` pattern.
- TMDB error or timeout silently omits the section; no error banner.
- React-query `staleTime`: 5 minutes for TMDB results (reduces external calls and respects TMDB rate limits), 60 seconds for library results (matches the existing `GlobalSearch` preview).

## Architecture

- No backend changes to existing endpoints. The frontend coordinates two parallel queries.
- Library on the results page: existing `useCatalogWindow` against `/api/v1/catalog?source=query`.
- Library in the Cmd+K dialog: existing `previewQuery` pattern using `fetchCatalogPage` against the same endpoint.
- TMDB: existing `useRequestSearch` hook against `/api/v1/requests/search`, used by both surfaces.
- Deduplication is handled server-side by the existing `enrichPage()` → `presence.Lookup()` flow on `/requests/search`. Client filters TMDB results where `availability == "available"` so they don't shadow library rows.
- A new `useCanRequest()` hook centralizes the gating logic. It reads admin settings (`RequestsEnabled`) and viewer policy (`EffectivePolicy.LimitMode`, quota state) and returns `{ enabled, disabledReason }`. When `enabled === false`, the TMDB query is not fired.

## Components

- `web/src/hooks/useCanRequest.ts` (new): exposes `{ enabled, disabledReason }` derived from settings + viewer policy.
- `web/src/components/RequestToAddSection.tsx` (new): renders the section in two variants:
  - `variant="dialog"` — compact row layout for `GlobalSearch`.
  - `variant="grid"` — poster grid using existing `RequestPosterCard` for `Catalog`.
- `web/src/components/GlobalSearch.tsx` (modified): wires the second query, passes results into `RequestToAddSection` with `variant="dialog"`.
- `web/src/pages/Catalog.tsx` (modified): renders `RequestToAddSection` with `variant="grid"` below the existing `ItemGrid` when the source is `query`.

## Edge cases

- TMDB returns only items already available in the library: section is omitted (after client filter).
- Library has hits but TMDB is still loading: library renders immediately; section shows a compact skeleton in its slot, then either renders or vanishes.
- Library has 0 hits and TMDB is still pending: the page suppresses the "No matches" empty state and shows a single loading indicator until TMDB resolves. Only after TMDB returns 0 (or errors) does the empty state render.
- TMDB query never fires (gated off): library follows its existing behavior including the standard empty state.
- Viewer logs out / profile changes mid-query: `useCanRequest()` re-evaluates and cancels the TMDB query if it becomes ineligible.
- Source is not `query` (e.g., `favorites`, `watchlist`, `history`, `section`): section never renders.

## Out of scope

- Backend changes to `/api/v1/catalog` or any merged endpoint.
- Inline request submission from search results (the detail page continues to own request creation).
- Surfacing requestable results in any non-search context (home, library browse, etc.).
- Person / cast results from TMDB. Only movie and series results are shown.

## Verification

Commands assume the repository root is the cwd.

- `cd web && pnpm run lint`
- `cd web && pnpm run format:check`
- Frontend component tests for `GlobalSearch`, `Catalog`, and `RequestToAddSection` covering: library-only results, library + TMDB, TMDB-only (library empty), both-empty, TMDB error, quota-disabled viewer, requests-globally-off.
- Manual smoke in the dev frontend: confirm library results are not delayed when TMDB is slow or errors; confirm the dialog and full-page surfaces both show the section under matching conditions.
