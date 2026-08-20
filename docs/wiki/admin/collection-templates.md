---
title: Collection Templates
description: Curated, one-click starting points for synced library collections.
summary: How the collection template gallery and template bundles work, what ships built-in, and how to add your own.
tags:
  - silo
  - docs
  - wiki
  - collections
  - admin
audience:
  - operator
last_reviewed: 2026-08-18
related:
  - ../index.md
---

# Collection Templates

Collection templates are pre-configured "blueprints" for a synced library collection. Picking
one in the admin UI seeds a new collection wired to TMDB, Trakt, or MDBList — including a
sensible default sync schedule — without the operator hand-typing presets, URLs, or cron
expressions. The built-in catalog ships over a hundred templates; **Template Bundles** let an
operator apply dozens of them across one or more libraries in a single action instead of
clicking through the gallery one card at a time.

Use a template when you want a synced shelf that follows a well-known feed (TMDB Trending, Trakt
Popular Shows, your favourite MDBList list, a TMDB franchise). Use the manual "Add Collection"
flow when you need a smart query, a hand-curated manual list, or a one-off MDBList URL you don't
want to reuse.

## Where to find them

Two entry points open the same gallery:

1. **Admin → Collections → "Browse Templates"** in the page header.
2. **Admin → Collections → "Add Collection" → "Browse Templates"** tile in the source-type
   chooser.

The gallery lists individual templates by category, and — when any are registered — a row of
**Template Bundle** cards above the search box. Picking an individual `tmdb`, `trakt`, or
`mdblist` template opens a confirmation drawer that posts to the matching import endpoint
(`/admin/collections/import/{tmdb,trakt,mdblist}`), so the resulting collection looks identical
to one created through the standard import form. Picking a `tmdb_discover` or `tmdb_collection`
template instead shows a read-only summary: those two sources are **bundle-only** — their filter
set or franchise ID ships from the server catalog and isn't editable inline, so the gallery
points you at Template Bundles to apply them.

## Source types

| Source | What it needs configured | Applied directly from the gallery? |
| --- | --- | --- |
| `tmdb` | A TMDB API key in Settings (`tmdb.api_key`, encrypted, restart-required). | Yes — opens the standard confirmation drawer. |
| `trakt` | A Trakt API app (`Client ID`/`Client Secret`) configured server-wide in Settings. Templates tagged "recommended" additionally need the chosen profile to have a Trakt account connected under **Settings → Watch Providers**. | Yes. |
| `mdblist` | Nothing required to *use* a list — fetching a public MDBList `/json` URL is an unauthenticated request. An optional MDBList API key in Settings only powers in-app list *search/browse* while picking a URL. | Yes. |
| `tmdb_discover` | Same TMDB API key as `tmdb` (same underlying client, hit via TMDB's `/discover` endpoint). | No — bundle-only; shows a read-only filter summary. |
| `tmdb_collection` | Same TMDB API key as `tmdb` (hit via TMDB's `/collection/{id}` endpoint). | No — bundle-only; shows a read-only summary. |

None of the five sources go through the plugin runtime — they're all built-in HTTP clients. The
plugin system is reserved for metadata/subtitle/watch-provider implementations, not these
collection sources.

## What ships built-in

The registry in
[`internal/collections/templates/builtin.go`](../../../internal/collections/templates/builtin.go)
currently ships over a hundred templates. Rather than enumerate them here (a list this size goes
stale the moment a template is added or repointed), browse the in-app gallery for the current,
authoritative listing — it renders straight from the server catalog. The templates group into a
handful of gallery categories:

| Category | Covers | A few examples |
| --- | --- | --- |
| Trending | TMDB and Trakt trending feeds. | Trending Today (All), Trending Movies This Week, Trakt Trending Shows. |
| Popular | TMDB/Trakt popularity feeds, an MDBList popularity chart, and TMDB Discover genre shelves sorted by current popularity (action, comedy, horror, sci-fi, and 15 more). | Popular Movies, IMDb MovieMeter Top 100, Popular Horror. |
| Streaming Services | Per-provider "what's on this service" lists and a curated "originals" set per provider. | Netflix Movies, Disney+ Shows, Apple TV+ (originals), HBO Max Originals. |
| Top Rated | TMDB/MDBList all-time top lists (including IMDb Top 250) and TMDB Discover genre shelves sorted by vote average. | Top Rated Movies, IMDb Top 250 Shows, Top Rated Documentary. |
| In Theaters / Upcoming / On Air | TMDB's now-playing, upcoming, and airing-today/this-week feeds. | Now Playing in Theaters, Upcoming Movies, Airing Today. |
| Editorial | The largest, most varied category: Trakt per-profile recommendations, hand-picked MDBList themes, Oscar/Golden Globe winners, yearly "Best of 2023–2025" roundups, seasonal and heritage-month lists (Halloween, Christmas, Pride Month, AAPI Heritage Month, …), studio/distributor catalogs (Criterion Collection, A24, Studio Ghibli), a PG-capped Kids Movies shelf, and TMDB franchise/saga collections (Star Wars, James Bond, Lord of the Rings, Jurassic Park, …) plus a generic franchise placeholder an admin fills in with any TMDB collection ID. | Trakt Recommended Movies, Oscar Winners, Best of 2024, Halloween, Criterion Collection, Star Wars Saga. |
| Custom | A "bring your own URL" MDBList template that opens the standard MDBList import form. | Custom MDBList. |

Templates default to 100 items. Finite canonical lists override that so the collection can hold
what the title promises: the IMDb Top 250 templates use 250, and catalog lists (Criterion
Collection, A24) ship with no limit at all so they match every owned title. Every template ships
with a conservative sync cadence — every 6 hours for trending, daily for popular and streaming
services, weekly for top-rated, editorial, awards, and seasonal picks — and a "featured" hint
where appropriate. All defaults are editable in the confirmation drawer before the collection is
created (for the three directly-created sources) or in the bundle apply view (for all five).

### Trakt "Recommended" templates

The Trakt Recommended templates require a profile that already has a Trakt account connected via
**Settings → Watch Providers**. The drawer asks for a profile picker before submitting; the
resulting collection is scoped to that profile's recommendations and re-syncs daily by default.

### A note on MDBList catalogs

The shipped MDBList templates point at public lists maintained by various MDBList users. They are
community-maintained, so the upstream URLs may eventually be retired or repointed by their owner.
When that happens the import job logs a warning during sync; the affected template can then be
retired or re-pointed in `builtin.go` (this has already happened once for the Criterion
Collection and A24 templates). Operators who maintain their own MDBList lists can register
additional templates against `templates.Default` at startup without forking this repo.

## Template Bundles

A bundle is a named, ordered set of built-in templates that can be applied together in one pass —
useful for seeding a whole starter library, a themed shelf set, or every TMDB genre at once
instead of adding templates one at a time. The catalog ships eight curated bundles plus one
auto-generated bundle:

| Bundle | Applies |
| --- | --- |
| Core Defaults | A focused starter set of movie and TV collections (trending, popular, top-rated, now-playing/upcoming, IMDb Top 250, a few MDBList editorial picks). |
| Streaming Originals | Provider-specific "originals" shelves (Apple TV+, Disney+, Max, Hulu, Netflix, Peacock, Prime Video) plus Shudder. |
| Awards & Yearly Picks | Oscar and Golden Globe winners, the yearly "Best of 2023/2024/2025" roundups, and the IMDb MovieMeter chart. |
| Seasonal Collections | Holiday (Halloween, Christmas, Valentine's Day, Easter, Thanksgiving, New Year) and heritage-month (AAPI, Latinx, Pride) shelves. |
| Studios & Labels | Criterion Collection, A24, IFC Films, Studio Ghibli. |
| Popular Genres | TMDB Discover genre shelves sorted by current popularity, plus the Kids Movies shelf. |
| Top Rated Genres | The same genre set, sorted by vote average with a vote-count floor. |
| Franchise Collections | TMDB saga/franchise collections (Star Wars, James Bond, Wizarding World, Fast & Furious, Lord of the Rings, The Hobbit, Jurassic Park, Pirates of the Caribbean, Mission: Impossible, MonsterVerse) plus a blank franchise placeholder. |
| All Defaults | Auto-generated at startup as the de-duplicated union of the eight bundles above. It does **not** include the plain Trakt trending/popular/recommended templates, the individual streaming-service "what's on this provider" lists, or the "bring your own URL" Custom MDBList template — those 16 templates are only reachable by picking them individually from the gallery. |

### Applying a bundle

Picking a bundle card opens an apply view with:

- **Libraries** — a multi-select; the bundle applies to every library chosen, filtered per
  template by media kind (movie-only templates skip TV-only libraries and vice versa).
- **Featured Sections** — optional pickers for a Home hero and one hero per selected library,
  each pointed at one of the bundle's eligible templates.
- **Delete Existing Server Collections** — a toggle that removes the operator's current
  collections in the selected libraries before applying the bundle's templates, for a clean
  reset instead of accumulating duplicates.
- **Preview** — runs the same logic as a dry run and shows what would be created, skipped, or
  deleted without writing anything.
- **Apply Defaults** — queues a background admin job (`POST
  /admin/collections/template-bundles/{bundleID}/apply-job`) rather than blocking the request,
  since a large bundle like All Defaults can create dozens of collections and queue their first
  syncs.

Re-applying a bundle is safe: existing collections are matched by their slugified title within
each library, so a second apply skips collections that already exist instead of duplicating them.
The TMDB franchise placeholder template ships with `collection_id: 0` and no sync schedule — after
applying it, edit the resulting collection's source config to set a real TMDB collection ID
before the first sync runs.

## How a single template becomes a collection

Picking a `tmdb`, `trakt`, or `mdblist` template opens a confirmation drawer with:

- **Libraries** — a multi-select (`library_ids: number[]`); the collection can be created in more
  than one library at once.
- **Title / Description** — pre-filled from the template, fully editable.
- **MDBList URL** — only shown when the template's source is MDBList; includes a browser to pick
  a public list without typing a URL.
- **Profile** — only shown when the template requires one (Trakt Recommended).
- **Poster** — use the template's default artwork or supply a custom image URL.
- **Max Items**, **Default Sort**, **Sync Schedule**, and **Featured** — pre-filled with the
  template's defaults, all editable.

Submitting the form posts to the matching import endpoint. The backend creates the collection,
runs the first sync, and the new shelf shows up in the collections list with its status set by
the initial sync run.

## Poster artwork

Built-in templates ship with poster artwork under
`web/public/images/collection-templates/`. Poster filenames match template IDs, for example
`tmdb_popular_movies.jpg`; raw generated plates live in
`web/public/images/collection-templates/raw/` so typography can be regenerated without
re-running image generation.

The poster style is intentionally close to Kometa/Plex collection posters:

- Use a 2:3 full-bleed cinematic poster composition.
- Use generic, original scenes only. Do not use copyrighted movie/show posters,
  recognizable actors, franchise characters, provider logos, or readable
  in-image text.
- Make the art context-specific. A horror template should look like horror; a
  documentary template should look investigative; a streaming-service template
  should have provider-flavoured ambience without provider branding.
- Do not use a generic category poster for many unrelated templates unless the
  context is truly identical.
- Keep typography deterministic outside the generated plate: media type in gold
  at top-left, collection title at bottom-left, no app branding, and no solid
  black lower-third text box. Use a subtle vignette and shadow/stroke for
  readability instead.

### Poster generation workflow

Generate the raw plate with an image-generation tool, then add deterministic typography locally:

1. Prompt for a 2:3 vertical, full-bleed cinematic collection poster plate.
   Include the template title and context, and explicitly require generic
   original art with no readable text, logos, watermarks, real posters,
   recognizable actors, franchise characters, or provider branding.
2. Copy the generated PNG into
   `web/public/images/collection-templates/raw/{template_id}.png`, resizing and
   center-cropping to `1024x1536`.
3. Create the final poster at
   `web/public/images/collection-templates/{template_id}.jpg`, resizing and
   center-cropping to `1000x1500`.
4. Add typography outside image generation: media type in gold at top-left,
   collection title at bottom-left, and the source label beneath it. Use a
   subtle dark vignette/overlay and text shadow or stroke for contrast.
5. Verify every built-in template has both files. The
   `internal/collections/templates` tests check this.

## Extending the registry

The catalog lives in
[`internal/collections/templates/builtin.go`](../../../internal/collections/templates/builtin.go),
fronted by a thread-safe registry in
[`internal/collections/templates/registry.go`](../../../internal/collections/templates/registry.go).
There is no JSON or config-file way to add templates — the catalog is Go code, registered at
process startup.

To add a template:

1. Append a `Template` struct to `builtinTemplates`, or call `templates.Register(...)` from your
   own startup code if you maintain a private fork. To group templates into a one-click bundle,
   add or extend an entry in `builtinTemplates`'s companion `builtinBundles` slice (or call
   `templates.RegisterBundle(...)`) — a bundle can't reference a template that requires a profile.
2. Run `go test ./internal/collections/templates/...` — the registry runs `validate(...)` on
   every template and bundle at registration time, so invalid presets, unknown media types,
   non-HTTP MDBList URLs, or a bundle referencing an unknown or profile-required template panic
   immediately and the tests catch them.
3. Restart the server. The `/admin/collections/templates` and `/admin/collections/template-bundles`
   endpoints pick up new entries without a frontend rebuild — the gallery renders straight from
   the server-supplied catalog.

### Source contracts

| Source | Required fields | Notes |
| --- | --- | --- |
| `tmdb` | `preset`, `media_type`; `time_window` for `trending` | Same shape the existing TMDB import endpoint accepts. `preset` must be one of `trending`, `popular`, `top_rated`, `now_playing`, `upcoming`, `airing_today`, `on_the_air`, each with its own allowed `media_type` values. |
| `trakt` | `preset` (`trending`, `popular`, or `recommended`), `media_type` (`movie` or `tv`) | Set `requires_profile: true` if and only if `preset` is `recommended` — validation rejects either mismatch. |
| `mdblist` | `url` (optional — empty means "ask the operator") | Empty URL renders an MDBList URL field in the drawer. A non-empty URL must be a valid `http`/`https` URL. |
| `tmdb_discover` | `media_type` (`movie` or `tv`), `sort_by` (one of TMDB's documented discover sort values) | Optional filters (genres, vote/runtime/date/certification bounds, original language) are validated for shape (non-negative counts, `YYYY-MM-DD` dates, 2-letter language codes, `gte <= lte`) but are otherwise passed straight to TMDB's `/discover` endpoint. |
| `tmdb_collection` | `collection_id` (>= 0) | `0` is a permitted placeholder/sentinel for a generic "fill in your own franchise" template; sync fails loudly until an admin edits the resulting collection's source config with a real TMDB collection ID. |

## API surface

- `GET /admin/collections/templates` returns the template catalog as
  `{ "categories": [{ "category", "label", "templates": [...] }] }`. The shape mirrors the Go
  types in `internal/collections/templates/templates.go`.
- `GET /admin/collections/template-bundles` returns the bundle catalog as
  `{ "bundles": [{ "id", "title", "description", "template_ids": [...] }] }`.
- `POST /admin/collections/template-bundles/{bundleID}/apply` applies (or, with `dry_run: true`,
  previews) a bundle synchronously and returns what was created, skipped, deleted, and featured.
- `POST /admin/collections/template-bundles/{bundleID}/apply-job` queues the same apply as a
  background admin job (no `dry_run` support) and returns `202 Accepted` with the job record; this
  is what the admin UI's "Apply Defaults" button uses.

All four endpoints sit inside the same `/admin/...` chain as the rest of the collection admin
handlers and require the admin role.

## Testing

- `go test ./internal/collections/templates/...` — registry validation, the built-in template and
  bundle catalogs (including sort-order band and poster-asset assertions), and category/lookup
  behaviour.
- `go test ./internal/api/handlers/ -run TestCollectionTemplateHandler` — template catalog HTTP
  handler smoke tests; `go test ./internal/api/handlers/ -run TestLibraryCollectionHandlerListsTemplateBundles`
  covers the bundle catalog endpoint.
- `pnpm vitest run src/components/CollectionTemplateGallery
  src/lib/collectionTemplates.test.ts` — frontend filter/search, bundle apply view, and
  source-dispatch tests.
