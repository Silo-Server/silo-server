# Plan: Personal + Server sections on the user Collections tab

Status: Draft
Branch: `feat/collections-server-section`
Owner: TBD
Commands assume the repository root is the cwd.

## Problem

The user-facing **Collections** tab on the home screen (`web/src/pages/Collections.tsx`)
only shows the signed-in profile's *personal* collections. For most users this list is
empty or nearly empty, so the tab looks broken or pointless.

Meanwhile, server-curated collections (admin-created "library collections") only appear
*inside each individual library's* Collections tab (`web/src/pages/LibraryCollections.tsx`,
served by `GET /library/{id}/collections`). There is no place in the app where a user can
see the collections curated across the whole server — they are effectively hidden behind
per-library navigation.

We want the top-level Collections tab to become the single home for collections:

1. **Your collections** — the user's personal/shared collections, with a large section
   title, shown at the top (current behavior, kept intact: grouping, drag-reorder, create,
   edit, sync, delete).
2. **Server collections** — below that, a clearly titled section aggregating the visible
   server (library) collections across every library the user can access, so they are no
   longer buried per-library.

## Current state (what exists today)

### Frontend
- `web/src/pages/Collections.tsx` — the top-level tab. Renders the page header + a
  `GroupedCollectionsBoard` of personal collections. Data via `useCollections()` /
  `useCollectionGroups()` (`web/src/hooks/queries/collections.ts`), both backed by
  `GET /collections` → `CollectionsListResponse { collections, groups }`.
- `web/src/pages/LibraryCollections.tsx` — per-library tab. Renders a poster grid
  (`CollectionPosterCard`) grouped by admin groups + ungrouped. Data via
  `useLibraryCollections(libraryId)` → `GET /library/{id}/collections` →
  `LibraryTabResponse { groups, ungrouped }`. **This is the visual we want to reuse for the
  server section** (poster cards, item-count badge, navigate to catalog href).
- `web/src/api/types.ts` — `Collection`, `CollectionGroup`, `CollectionsListResponse`,
  `LibraryTabResponse`, `LibraryTabGroup`, `LibraryTabCollection`, `LibraryTabUngrouped`.
- Catalog hrefs: `buildLibraryCollectionCatalogHref(id, title)` and
  `buildUserCollectionCatalogHref(id, title)` in `web/src/pages/catalogSearchParams.ts`.

### Backend
- `internal/api/handlers/collections.go` — personal collection handlers under `/collections`
  (registered in `internal/api/router.go` ~lines 1700-1726, behind `RequireProfile`).
- `internal/api/handlers/library_collections.go`:
  - `HandleListLibraryCollections` (line 813) — builds the per-library `LibraryTabResponse`.
  - `requestCanAccessLibrary` (line 3311) — checks `access.GetScope(ctx).AllowedLibraryIDs`
    (nil ⇒ access to all libraries).
  - `LibraryCollectionRepository.ListAll(ctx, libraryID *int, opts)`
    (`internal/catalog/library_collection_repo.go:368`) — **with `libraryID == nil` it
    already returns server collections across all libraries** (uses
    `libraryCollectionScopeFallbackJoin`), filtering `visibility = 'visible'` unless
    `IncludeHidden`. This is the aggregation primitive we need.
  - `presignGPURL` / `toLibraryCollectionResponses` — turn stored poster paths into signed
    URLs for the response.

## Design decision: aggregate on the server, new endpoint

We add a new **user-facing aggregate endpoint** rather than fanning out N per-library calls
from the client (avoids N round-trips, keeps access/visibility filtering server-side, and
matches the "Performance first / Reliability first" priorities in `CLAUDE.md`).

### New endpoint

`GET /collections/server` (registered next to the other `/collections` routes in
`internal/api/router.go`, behind `RequireProfile`).

Response shape (new types, mirrors the poster-card needs without per-library group noise):

```jsonc
{
  "libraries": [
    {
      "library_id": 3,
      "library_name": "Movies",
      "total_count": 80,           // total visible server collections in this library
      "collections": [            // CAPPED teaser slice (see "Scale & pagination")
        {
          "id": "...",
          "title": "...",
          "poster_url": "https://signed...",
          "poster_thumbhash": "...",
          "item_count": 42,
          "featured": false
        }
      ]
    }
  ]
}
```

Grouping by library (with `library_name`) is the recommended default: it lets the frontend
render "Server collections" as one **horizontal scrollable row per library**, which keeps a
large catalog legible. (Alternative: a single flat list — simpler UI, but loses provenance
and the per-library "See all" affordance. Recommend per-library grouping.)

### Scale & pagination (many collections per library)

The server section is a **discovery** surface aggregating across *all* accessible libraries
at once, so a full wrapping grid per library does not scale (e.g. 5 libraries × 80
collections = 400 stacked tiles). Decisions:

- **Render each library as a horizontal carousel row**, not a wrapping grid. Heading = the
  library name + a **"See all →"** link.
- **Cap the response** at ~20 collections per library and include `total_count`. Ordering of
  the capped slice follows the repo's existing `featured DESC, sort_order ASC, title ASC`
  ("best first"), so the teaser row surfaces featured/curated collections.
- **"See all" links to the existing per-library Collections tab**
  (`/library/{id}` Collections tab, served by the unchanged
  `GET /library/{id}/collections`), which already renders the full wrapping grid with admin
  grouping. We do **not** build a new full-aggregate view — reuse the canonical
  "show everything for this library" surface.
- This caps payload size and the per-library query cost, and keeps the home tab fast
  ("Performance first").

### API contract impact

- `GET /collections` (personal) — **unchanged**.
- `GET /library/{id}/collections` (per-library tab, "See all" target) — **unchanged**;
  reused as-is.
- `GET /collections/server` — **new, additive**. No existing response changes; no
  Android/Apple contract break.

### Why a separate endpoint, not extending `GET /collections`

It is tempting to make `GET /collections` return both personal and server collections in one
call. We deliberately do **not**, for three reasons:

- **Client safety / semantics.** `GET /collections`'s `collections[]` array is the user's
  *editable, personal* shelves — web and the Android/Apple clients render reorder/edit/
  delete/sync affordances on it. Mixing read-only server collections into that same array is
  a *silent semantic break*: the JSON still parses, so nothing errors, but clients would
  offer "delete"/"reorder" on collections the user can't mutate (→ 403/404). A new sibling
  field (`{ collections, groups, server }`) would be additive and parse-safe, but still
  couples discovery into the personal-management contract.
- **Cache / refetch lifecycle.** `GET /collections` is the heavily-mutated query —
  invalidated on every create, edit, reorder, group change, and sync (via
  `invalidateUserCollectionQueries`). Server collections don't change on a personal reorder,
  so folding them in would refetch the whole server-wide catalog on every personal mutation
  ("Performance first" violation). Separate endpoints ⇒ independent cache keys + lifecycles.
- **Zero downstream risk.** A brand-new `/collections/server` endpoint cannot break existing
  clients, because nothing calls it yet. Consolidating into one call later is a *deliberate
  follow-up requiring coordinated Android/Apple work* — and must use a distinct field, never
  overload `collections[]`.

### Handler logic (`HandleListServerCollections`)

1. Resolve accessible library IDs: read `access.GetScope(ctx)`. If `AllowedLibraryIDs ==
   nil`, the user can see all libraries → list all of them; else restrict to that set.
2. List visible server collections:
   - Simplest correct approach: call `repo.ListAll(ctx, nil, ListLibraryCollectionsOptions{})`
     once (server-wide, visible-only), then **filter each collection to libraries the user
     can access** and bucket by library. This requires knowing each collection's
     library membership (`library_collection_libraries`). If `ListAll` does not already
     return membership, either (a) add a per-library loop calling
     `repo.ListByLibrary(ctx, libID, opts)` for each accessible library (bounded by library
     count, each query already does the grouping/sort), or (b) extend the repo to return
     library membership. **Recommended: per-accessible-library loop** — reuses the existing,
     tested `ListByLibrary` path and naturally yields the per-library buckets and respects
     scope. Deduplicate collections that span multiple libraries if we choose flat output;
     for per-library output, a collection legitimately appears under each of its libraries.
3. For each library, set `total_count` to the full visible count, then **cap
   `collections` to ~20** (keep the repo's `featured DESC, sort_order ASC, title ASC`
   ordering so featured/curated collections lead). Define the cap as a single named
   constant.
4. Resolve library display names (catalog/library store lookup) for `library_name`.
5. Presign poster URLs via the existing `presignGPURL` helper — only for the capped slice,
   so we never presign 100 posters we won't send.
6. Skip empty libraries. Return `200` with `{ libraries: [] }` when nothing is visible.

Reuse existing helpers (`presignGPURL`, sort helpers like `applyCollectionSort`) rather than
duplicating logic. Add the new response structs next to the existing
`libraryTabResponse` types in `library_collections.go`, or in `collections.go` if cleaner —
keep them in the package that owns the behavior.

## Frontend changes

### Types (`web/src/api/types.ts`)
Add:
```ts
export interface ServerCollectionsLibrary {
  library_id: number;
  library_name: string;
  total_count: number; // total visible; collections[] is a capped teaser slice
  collections: LibraryTabCollection[]; // reuse existing shape
}
export interface ServerCollectionsResponse {
  libraries: ServerCollectionsLibrary[];
}
```

### Query hook (`web/src/hooks/queries/collections.ts` + `keys.ts`)
- Add `collectionKeys.server()` to `web/src/hooks/queries/keys.ts`.
- Add `useServerCollections()` → `GET /collections/server` →
  `ServerCollectionsResponse`. Independent query key from `/collections`, so the personal
  section keeps its single round-trip and the server section loads in parallel.

### Page (`web/src/pages/Collections.tsx`)
Restructure into two titled sections inside the existing `page-shell`:

1. **"Your collections"** section
   - Add a large section title (e.g. `<h2 class="page-title ...">Your collections</h2>`)
     above the existing `GroupedCollectionsBoard` / empty-state block. Keep all current
     personal-collection behavior unchanged (create, group, reorder, sync, delete).
   - The page-level `<h1>Collections</h1>` header + subtitle + action buttons stay at the
     very top.

2. **"Server collections"** section (new)
   - Title `<h2>Server collections</h2>` with a short subtitle ("Curated across every
     library on this server").
   - Use `useServerCollections()`. Render **one horizontal scrollable row per library**
     (carousel), not a wrapping grid. Each poster card reuses the **same visual as
     `LibraryCollections.tsx`** — extract `CollectionPosterCard` into a shared component so
     both pages use it (avoids duplicate logic per `CLAUDE.md` maintainability rule).
     Proposed location: `web/src/components/collections/CollectionPosterCard.tsx`.
   - For each `library` bucket: a sub-heading row with `library_name` and a **"See all →"**
     link routing to that library's existing Collections tab (the unchanged
     `GET /library/{id}/collections` surface). Show "See all" only when
     `total_count > collections.length`; optionally include the count, e.g. "See all (80)".
   - Cards navigate via `buildLibraryCollectionCatalogHref(id, title)` and show the
     item-count badge. These are admin/library collections (`kind="regular"`); the
     sidebar-pin affordance can remain — pass `library.library_id` as `libraryId`.
   - Use the project's existing horizontal-row/carousel pattern if one exists (check
     `web/src/components` for a shelf/row component used on the home screen) rather than
     hand-rolling overflow scrolling; reuse it for consistent snap/scroll behavior.
   - Loading: skeleton row(s) (reuse the existing skeleton block pattern).
   - Empty: if `libraries` is empty, render nothing (or a muted "No server collections yet"
     line) — do not show a scary empty state, since the personal section above may have
     content.

### Refactor note
Extracting `CollectionPosterCard` is the only shared-logic extraction required. Keep the
extraction behavior-identical for `LibraryCollections.tsx` (props: `collection`, `kind`,
`libraryId`). The per-library tab keeps its wrapping grid; only the new server section uses
horizontal rows.

## Out of scope / non-goals
- No changes to how admin/library collections are created or to per-library tabs.
- No reordering/editing of server collections from the user Collections tab (read-only).
- No new visibility model — we honor existing `visibility = 'visible'` + access scope.

## Risks / follow-ups
- **Access scope correctness**: must not leak collections from libraries outside
  `AllowedLibraryIDs`. Mitigate by driving the query from accessible library IDs, and add a
  test for a restricted-scope user.
- **Collections spanning multiple libraries** appear once per library in per-library output;
  confirm this is the desired UX (vs. dedup). Decide before implementation.
- **Performance**: per-accessible-library loop is N queries. Acceptable for typical library
  counts; if N grows large, revisit with a single membership-aware query. Add an index check
  on `library_collection_libraries(library_id)` (likely already present).
- **Client parity**: this is a new server endpoint and a web-only UI change. Android/Apple
  clients have their own collections surfaces — file follow-up issues if they should mirror
  the personal+server split. No API contract break (purely additive endpoint).
- **Empty-state UX** for users with no personal AND no server collections — ensure the page
  still reads sensibly.

## Verification
- Backend: `go build ./...`, `go vet ./...`, and a handler/unit test for
  `HandleListServerCollections` covering (a) full access, (b) restricted scope, (c) empty.
- Frontend: `cd web && pnpm run lint && pnpm run format:check`; `make verify-local-paths`.
- Manual: log in as a normal profile with empty personal collections; confirm the
  "Your collections" section shows the empty/create state and the "Server collections"
  section lists library collections grouped by library with correct posters, counts, and
  working navigation into each collection's catalog view.

## Implementation order
1. Backend: response types + `HandleListServerCollections` + route registration.
2. Backend test for access-scope filtering.
3. Frontend: types + `useServerCollections` hook + `collectionKeys.server()`.
4. Frontend: extract shared `CollectionPosterCard`; refactor `LibraryCollections.tsx`.
5. Frontend: restructure `Collections.tsx` into the two titled sections.
6. Lint/format/build/manual verification.
