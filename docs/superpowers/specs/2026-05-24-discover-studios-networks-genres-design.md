# Discover: Studios, Networks, and Genres

Status: design approved 2026-05-24

## Dependency

This feature extends the media request system spec'd in
`docs/superpowers/specs/request-system.md` (currently on branch
`t3code/df875f01`). It must land on top of that branch — it depends on
`internal/requests/`, `internal/metadata/tmdb/`, `Requests.tsx`, and the
existing `/api/v1/requests/discover/*` route prefix.

## Goal

Add an Overseerr-style browse-by-brand experience to the request discover
surface. Users land on `/requests`, see existing Trending / Popular / Upcoming /
On Air carousels, and below them three new carousels of brand cards:

- **Studios** — Disney, Pixar, Marvel, etc. Click → movie browse page.
- **Networks** — Netflix, HBO, Apple TV+, etc. Click → series browse page.
- **Genres** — Action, Comedy, Drama, etc. Click → genre browse page with
  Movies / Series tabs.

Each brand card is a curated entry tied to a TMDB company, network, or genre
ID. Click-through lands on a paginated, sortable grid of TMDB results enriched
with Silo availability and request state — the same enrichment used by the
existing six discovery sections.

## V1 Scope

- A bundled, compile-time list of ~10 studios, ~10 networks, and ~8 genres.
- Logos fetched from TMDB at runtime (`company.logo_path`,
  `network.logo_path`), cached server-side for 24h.
- Genre cards rendered with a gradient + display name (TMDB has no logos for
  genres).
- Three new discover list endpoints returning brand cards.
- Three new browse endpoints returning enriched paginated TMDB results.
- Sort by Popularity (default), Vote Average, or Release Date.
- Studios browse: movies only. Networks browse: series only. Genres browse:
  Movies / Series tabs, Series tab hidden when the genre has no TV equivalent.

## Non-Goals (v1)

- Admin UI for editing the bundled list — compile-time only.
- Cross-filter on the browse page (e.g., Marvel + Action).
- Infinite scroll — page-based pagination only.
- Studios browse showing series, networks browse showing movies — strict 1:1
  media-type mapping.
- Per-user favorites or pinned studios.
- Search-within-browse-page.
- Backdrop variants for genre cards — gradient + text only.
- Internationalized brand display names — English only.

## Architecture

New domain code lives under `internal/requests/` alongside the existing request
system files. No new top-level package, no sub-package — keep the layout flat
to match the rest of `internal/requests/`.

New files:

- `discover_bundle.go` — hardcoded `BundledStudio`, `BundledNetwork`,
  `BundledGenre` slices. Slug → entity lookup helpers.
- `discover_logo_cache.go` — in-memory `map[int]string` for TMDB company and
  network `logo_path`. Lazy fetch, 24h TTL, singleflight to dedupe concurrent
  misses.
- `discover_brand.go` — `ListStudios`, `ListNetworks`, `ListGenres`,
  `BrowseStudio`, `BrowseNetwork`, `BrowseGenre` methods. Reuses
  `enrichResults` from the existing service for availability + request state.

Extensions to existing code:

- `internal/metadata/tmdb/client.go`
  - Add `WithCompanies []int` and `WithNetworks []int` to `DiscoverParams`.
  - Add `GetCompany(ctx, id)` and `GetNetwork(ctx, id)` helpers that return the
    canonical entity (we only need `logo_path` and `name` from each).
- `internal/api/handlers/requests.go` — six new handler methods.
- `internal/api/router.go` — wire the six new routes under
  `/api/v1/requests/discover/...`.

No new database tables. No migrations. Stateful caches are process-local.

## Data Model

### Go types (constants and request/response shapes)

```go
type BundledStudio struct {
    TMDBID      int    // TMDB company ID
    Slug        string // URL slug, e.g. "marvel-studios"
    DisplayName string
    BrandColor  string // hex, e.g. "#ed1d24"
}

type BundledNetwork struct {
    TMDBID      int
    Slug        string
    DisplayName string
    BrandColor  string
}

type BundledGenre struct {
    Slug         string
    DisplayName  string
    GradientFrom string // hex
    GradientTo   string // hex
    MovieID      int    // TMDB movie genre ID, always set in v1
    SeriesID     int    // TMDB tv genre ID, 0 if no TV equivalent
}

type DiscoverBrandCard struct {
    TMDBID       int     `json:"tmdb_id,omitempty"`     // omitted for genres
    Slug         string  `json:"slug"`
    DisplayName  string  `json:"display_name"`
    BrandColor   string  `json:"brand_color,omitempty"`   // studios/networks
    LogoURL      *string `json:"logo_url,omitempty"`      // studios/networks; null on lookup failure
    GradientFrom string  `json:"gradient_from,omitempty"` // genres
    GradientTo   string  `json:"gradient_to,omitempty"`   // genres
    SeriesSupported bool `json:"series_supported,omitempty"` // genres; true iff SeriesID > 0
}

type DiscoverBrowseResponse struct {
    Kind        string              `json:"kind"` // "studio" | "network" | "genre"
    Slug        string              `json:"slug"`
    DisplayName string              `json:"display_name"`
    BrandColor  string              `json:"brand_color,omitempty"`
    LogoURL     *string             `json:"logo_url,omitempty"`
    MediaType   MediaType           `json:"media_type"`
    Sort        string              `json:"sort"`
    Page        int                 `json:"page"`
    TotalPages  int                 `json:"total_pages"`
    Results     []MediaResult       `json:"results"` // existing internal/requests.MediaResult type, same as search/discover
}
```

### Bundled v1 contents

Exact TMDB IDs verified during implementation against `/company/{id}` and
`/network/{id}` responses. The lists below are starting defaults; the team can
adjust during plan review.

**Studios (10):** Walt Disney Pictures, Pixar, Marvel Studios, Lucasfilm,
Warner Bros. Pictures, Universal Pictures, Paramount Pictures, Sony Pictures,
20th Century Studios, Studio Ghibli.

**Networks (10):** Netflix, Disney+, Apple TV+, HBO, Hulu, Amazon Prime Video,
Max, Paramount+, BBC, FX.

**Genres (8):**

| Slug | Display | Movie ID | Series ID |
|---|---|---|---|
| `action` | Action | 28 | 10759 *(Action & Adventure)* |
| `comedy` | Comedy | 35 | 35 |
| `drama` | Drama | 18 | 18 |
| `sci-fi` | Sci-Fi | 878 | 10765 *(Sci-Fi & Fantasy)* |
| `horror` | Horror | 27 | 0 *(no TV equivalent)* |
| `romance` | Romance | 10749 | 0 *(no TV equivalent)* |
| `animation` | Animation | 16 | 16 |
| `documentary` | Documentary | 99 | 99 |

Genres with `SeriesID = 0` set `SeriesSupported = false`; the Series tab is
hidden on the browse page.

## API Surface

All routes are profile-scoped, served under the existing
`/api/v1/requests/discover/` prefix, with the same auth middleware as existing
discover routes.

### List endpoints

```
GET /api/v1/requests/discover/studios
GET /api/v1/requests/discover/networks
GET /api/v1/requests/discover/genres
```

Each returns `{ "studios" | "networks" | "genres": [DiscoverBrandCard, ...] }`
in bundle order. A failed logo lookup yields a card with `logo_url: null` —
the response is not failed wholesale.

### Browse endpoints

```
GET /api/v1/requests/discover/browse/studio/{slug}?page=1&sort=popularity
GET /api/v1/requests/discover/browse/network/{slug}?page=1&sort=popularity
GET /api/v1/requests/discover/browse/genre/{slug}?media_type=movie&page=1&sort=popularity
```

- `sort` ∈ `popularity` (default), `vote_average`, `release_date`. All three
  are descending — most popular / highest rated / most recent first. There is
  no ascending variant in v1.
  - `popularity` → TMDB `popularity.desc`
  - `vote_average` → TMDB `vote_average.desc` with `vote_count.gte=100` to
    filter low-vote noise
  - `release_date` → TMDB `primary_release_date.desc` (movies) or
    `first_air_date.desc` (series)
- `media_type` ∈ `movie`, `series`.
  - **Required** on `genre` browse.
  - **Omitted** on `studio` browse (always movie) and `network` browse (always
    series).
  - Invalid combo (e.g., `media_type=series` for a genre with
    `SeriesSupported = false`) → 400.
- `{slug}` is the bundle slug. Unknown slug → 404.
- `page` defaults to 1.

Response: `DiscoverBrowseResponse` — see Go types above. `results` items share
the shape of existing search/discover results and carry the same `availability`
and `request: { status, requestable }` fields.

## Caching

| Resource | Cache | Lifetime |
|---|---|---|
| Studio/network `logo_path` | In-memory `map[int]string`, singleflight on miss | 24h |
| Browse responses (TMDB discover results) | Existing TMDB response cache wrapper | 15 min |
| List responses (`/discover/studios` etc.) | In-memory, keyed by `(kind, locale)` | 24h |

All caches are process-local. Restart clears them and they re-fill lazily.

## Frontend

### New components (`web/src/components/`)

- `BrandCarousel.tsx` — horizontal-scroll carousel for brand cards. Same
  scroll behavior as the existing `MediaCarousel`, slot dimensions differ
  (~140x80 instead of poster aspect ratio).
- `BrandCard.tsx` — renders one brand card. Props:
  `{ kind, slug, displayName, brandColor?, logoUrl?, gradientFrom?, gradientTo?, movieSupported?, seriesSupported? }`.
  Studios/networks render with `brandColor` + `<img src={logoUrl}>` (or
  centered `displayName` text fallback if `logoUrl` is null). Genres render
  with a CSS gradient (`gradientFrom` → `gradientTo`) and centered display
  name. Click → `useNavigate()` to the matching browse route.

### `Requests.tsx` changes

Append three new sections after the existing six discovery carousels:

1. `BrandCarousel` for Studios — backed by `useDiscoverStudios()`.
2. `BrandCarousel` for Networks — backed by `useDiscoverNetworks()`.
3. `BrandCarousel` for Genres — backed by `useDiscoverGenres()`.

Skeleton states reuse the existing `Skeleton` import. If a list endpoint
fails, only that carousel shows an inline retry — the rest of the page
remains.

### New routes (`web/src/App.tsx`)

```
/requests/browse/studio/:slug
/requests/browse/network/:slug
/requests/browse/genre/:slug
```

All three render `RequestBrowse.tsx`. The page reads `kind` from the route,
`slug` from `useParams`, and `media_type` + `sort` + `page` from query string.

### New page (`web/src/pages/RequestBrowse.tsx`)

Layout:

- Header: Back link, brand card preview (or gradient for genres), display
  name, result count, and a sort `Select` dropdown.
- Genre pages only: shadcn `Tabs` for Movies / Series, with the Series tab
  hidden if `series_supported = false`. The active tab drives the
  `media_type` query param.
- Grid: reuses existing `RequestPosterCard` — no changes to that component
  since the browse response item shape mirrors the search response item shape.
- Pagination: page-based with Prev/Next buttons matching the `searchPage`
  pattern in `Requests.tsx`.

Empty/error states:

- Unknown slug → "Studio/Network/Genre not found" + link back to `/requests`.
- Empty results → "Nothing matched — try a different sort."
- Network error → existing toast pattern from the request hooks.

### New hooks (`web/src/hooks/queries/requests.ts`)

- `useDiscoverStudios()`, `useDiscoverNetworks()`, `useDiscoverGenres()` —
  24h stale time matching the server cache.
- `useRequestBrowse({ kind, slug, mediaType?, sort, page })` — 60-90s stale
  time so request state stays fresh.

### New types (`web/src/api/types.ts`)

- `DiscoverStudio`, `DiscoverNetwork`, `DiscoverGenre`,
  `DiscoverBrowseResult`.

### Sidebar

No changes. The existing "Requests" sidebar entry covers the new routes.

## Failure modes and edge cases

- **Logo fetch fails for one entity** — card returned with `logo_url: null`;
  rest of the list unaffected. Logged as a warning.
- **Genre has no TV equivalent** — `series_supported: false`; Series tab
  hidden; `media_type=series` requests for that genre return 400.
- **Browse returns zero results** — empty state on the page; carousel cards
  are not pre-checked for result counts.
- **Pagination past TMDB's page-500 cap** — `total_pages` reflects TMDB's
  response; the Next button disables when `page == total_pages`.
- **TMDB rate-limited** — existing `retryAfterOrDefault` backoff in the TMDB
  client applies. Sustained rate-limiting returns 503; cached carousels are
  unaffected.
- **`requests_enabled = false`** — all new endpoints return 403 like the rest
  of `/requests/*`. Sidebar entry is hidden by existing behavior.

## Testing

Backend:

- `discover_bundle_test.go` — slug lookups, unknown slug, integrity (no
  duplicate slugs, every entry has required fields).
- `discover_logo_cache_test.go` — TTL expiry, miss returns `nil` path, parallel
  access does not double-fetch (singleflight).
- `internal/metadata/tmdb/client_test.go` — extend with `WithCompanies` and
  `WithNetworks` query construction cases.
- `internal/requests/service_test.go` — extend with `ListStudios/Networks/
  Genres` (bundled list returned with logo lookups stubbed) and
  `BrowseStudio/Network/Genre` (enriched TMDB results, verify `availability` +
  `request` state).
- `internal/api/handlers/requests_test.go` — six new endpoints. Cover auth,
  invalid `media_type` on genre browse, unknown slug → 404, unknown `sort` →
  400, `requests_enabled = false` → 403.

Frontend: manual verification via `make dev-frontend` against a backend with
the new endpoints. Existing request system commits did not add frontend unit
tests; this feature follows suit.

## Acceptance criteria

- `/requests` shows Studios, Networks, and Genres carousels below the existing
  six discovery sections.
- Clicking a studio card navigates to `/requests/browse/studio/{slug}` and
  renders a paginated, sortable grid of movies enriched with availability and
  request state.
- Clicking a network card navigates to `/requests/browse/network/{slug}` with
  series.
- Clicking a genre card navigates to `/requests/browse/genre/{slug}` with
  Movies/Series tabs; the Series tab is hidden when `series_supported = false`.
- Sort options (Popularity, Vote Average, Release Date) work on all browse
  pages and update the URL.
- A card with a failed logo lookup falls back to text and does not break the
  carousel.
- Existing request creation, status, quota, and Radarr/Sonarr fulfillment flows
  are unchanged and still pass their tests.
- `make lint`, `cd web && pnpm run lint`, and `cd web && pnpm run format:check`
  all pass.
