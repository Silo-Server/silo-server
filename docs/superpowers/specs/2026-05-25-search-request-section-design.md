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

Discovery eligibility (whether the TMDB query fires) is separate from submission eligibility (whether the row's request CTA is active):

- **Discovery eligibility** is gated only by global/identity preconditions: admin `RequestsEnabled = true`, the viewer is authenticated, and has a profile. If any of these is false, the TMDB query does not fire and the section is not rendered.
- **Submission eligibility** is per-viewer policy: quota-exhausted, individually blocked (`UserLimit.LimitMode = "blocked"`), or otherwise restricted. When discovery is allowed but submission is not, the section still renders, each row's request affordance is disabled, and a tooltip surfaces the reason. Rows remain clickable and still navigate to the detail page, which is responsible for displaying the full policy state.

This keeps search-side UX consistent with what the detail page would show for the same viewer.

### Performance

- Library results never wait on TMDB. The two queries fire concurrently from the client; the library section paints as soon as FTS returns.
- TMDB query is debounced at 400ms; library query stays at the current 200ms.
- TMDB query is cancelled in-flight when the query string changes. This requires extending `useRequestSearch` to accept and forward `{ signal }` to `api` (it does not today); see Architecture.
- TMDB error or timeout silently omits the section; no error banner.
- React-query `staleTime`: 5 minutes for TMDB results (reduces external calls and respects TMDB rate limits), 60 seconds for library results (matches the existing `GlobalSearch` preview). The 5-minute window is only safe because the cache key includes viewer identity (see Architecture); cross-viewer reuse is impossible.

## Architecture

- No backend changes to existing endpoints. The frontend coordinates two parallel queries.
- Library on the results page: existing `useCatalogWindow` against `/api/v1/catalog?source=query`.
- Library in the Cmd+K dialog: existing `previewQuery` pattern using `fetchCatalogPage` against the same endpoint.
- TMDB: existing `useRequestSearch` hook against `/api/v1/requests/search`, used by both surfaces — see required extensions below.
- Deduplication is handled server-side by the existing `enrichPage()` → `presence.Lookup()` flow on `/requests/search`. Client filters TMDB results where `availability == "available"` so they don't shadow library rows.

### Gating hook (`useCanRequest`)

The new hook splits its return into two independent signals:

- `discoveryEnabled: boolean` — true when admin `RequestsEnabled = true` AND the viewer is authenticated with a profile. This is the only signal that controls whether the TMDB query fires.
- `submitDisabledReason: string | null` — null when the viewer can submit; otherwise one of `"blocked"`, `"quota_exhausted"`, or a future reason key. Passed through `RequestToAddSection` to per-row UI to disable the request CTA and populate its tooltip.

Per-viewer policy state (`EffectivePolicy.LimitMode`, quota counters) feeds `submitDisabledReason` and is never used to suppress the query.

### `useRequestSearch` extensions

The existing hook is reused but must be extended before it can back this feature safely:

- **Pass through `{ signal }`**: the query function currently does not accept the react-query `signal`. Update it to accept the signal and forward it to `api` so in-flight TMDB requests are cancelled on query change, unmount, or viewer change.
- **Key by viewer identity**: extend `requestKeys.search(...)` to include the active `profile_id` (and `user_id` if profile alone is insufficient to identify the policy holder). This prevents cached results from being served across viewer changes and makes the 5-minute `staleTime` safe.
- **Invalidate on policy or identity change**: invalidate `requestKeys.search()` queries when any of the following occurs in the SPA: login/logout, profile switch, admin `RequestsEnabled` toggle, `UserLimit` mutation affecting the current viewer, or quota reset/refresh. The invalidation hooks live alongside the existing auth/profile/settings stores.

## Components

- `web/src/hooks/useCanRequest.ts` (new): exposes `{ discoveryEnabled, submitDisabledReason }` derived from settings + viewer identity + policy as described in Architecture.
- `web/src/hooks/queries/useRequests.ts` (modified): extend `useRequestSearch` and `requestKeys.search(...)` to accept/forward `{ signal }`, include viewer identity in the query key, and expose invalidation helpers used by the auth/profile/settings stores.
- `web/src/components/RequestToAddSection.tsx` (new): renders the section in two variants:
  - `variant="dialog"` — compact row layout for `GlobalSearch`.
  - `variant="grid"` — poster grid using existing `RequestPosterCard` for `Catalog`.
  Accepts `submitDisabledReason` and propagates it to per-row CTAs.
- `web/src/components/GlobalSearch.tsx` (modified): wires the second query, passes results into `RequestToAddSection` with `variant="dialog"`.
- `web/src/pages/Catalog.tsx` (modified): renders `RequestToAddSection` with `variant="grid"` below the existing `ItemGrid` when the source is `query`.

## Edge cases

- TMDB returns only items already available in the library: section is omitted (after client filter).
- Library has hits but TMDB is still loading: library renders immediately; section shows a compact skeleton in its slot, then either renders or vanishes.
- Library has 0 hits and TMDB is still pending: the page suppresses the "No matches" empty state and shows a single loading indicator until TMDB resolves. Only after TMDB returns 0 (or errors) does the empty state render.
- TMDB query never fires (discovery gated off): library follows its existing behavior including the standard empty state.
- Viewer logs out, switches profile, or admin disables `RequestsEnabled` mid-query: `useCanRequest()` re-evaluates and `discoveryEnabled` flips to false; the in-flight TMDB request is cancelled via its forwarded `signal`, and cached entries under the previous viewer identity are invalidated so they cannot be re-served.
- Admin updates `UserLimit` for the current viewer while results are cached: the settings/limit mutation triggers a `requestKeys.search()` invalidation; the next paint re-fetches with the new `submitDisabledReason`.
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
- Frontend component tests for `GlobalSearch`, `Catalog`, and `RequestToAddSection` covering: library-only results, library + TMDB, TMDB-only (library empty), both-empty, TMDB error, blocked viewer (section renders, CTAs disabled), quota-exhausted viewer (section renders, CTAs disabled), requests-globally-off (no TMDB query fired, no section).
- Hook tests for `useCanRequest` across the matrix of `RequestsEnabled`, auth state, profile presence, and policy states, asserting that `discoveryEnabled` and `submitDisabledReason` are independent.
- Hook/integration tests for the extended `useRequestSearch`: confirm `signal` forwarding cancels in-flight requests on query change, confirm cache entries are not shared across `profile_id` keys, and confirm the relevant store mutations invalidate `requestKeys.search()`.
- Manual smoke in the dev frontend: confirm library results are not delayed when TMDB is slow or errors; confirm the dialog and full-page surfaces both show the section under matching conditions.
