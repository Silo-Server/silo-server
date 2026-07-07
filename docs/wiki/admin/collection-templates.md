---
title: Collection Templates
description: Curated, one-click starting points for synced library collections.
summary: How the collection template gallery works, what ships built-in, and how to add your own.
tags:
  - silo
  - docs
  - wiki
  - collections
  - admin
audience:
  - operator
last_reviewed: 2026-05-05
related:
  - ../index.md
---

# Collection Templates

Collection templates are pre-configured "blueprints" for a synced library
collection. Picking one in the admin UI seeds a new collection wired to TMDB,
Trakt, or MDBList — including a sensible default sync schedule — without the
operator hand-typing presets, URLs, or cron expressions.

They are the collection-side analogue of the
[Section Recipe Gallery](../../superpowers/specs/sections-recipes.md): a
catalog of opinionated starting points that live alongside the lower-level
"Add Collection" flow.

## When to reach for a template

Use a template when you want a synced shelf that follows a well-known feed
(TMDB Trending, Trakt Popular Shows, your favourite MDBList list). Use the
manual flow ("Add Collection") when you need a smart query, a hand-curated
manual list, or a custom MDBList URL that you do not want to reuse.

## Where to find them

Two entry points open the same gallery:

1. **Admin → Collections → "Browse Templates"** in the page header.
2. **Admin → Collections → "Add Collection" → "Browse Templates"** tile in the
   source-type chooser.

Both flows hand the chosen template to the existing import endpoint
(`/admin/collections/import/{tmdb,trakt,mdblist}`), so the resulting
collection looks identical to one created through the standard import form.

## What ships built-in

The default registry covers TMDB and Trakt's public discovery feeds plus a
"bring your own URL" MDBList template:

| Category           | Templates                                                                                                       |
| ------------------ | --------------------------------------------------------------------------------------------------------------- |
| Trending           | TMDB Trending Today (mixed), Trending Movies (week), Trending TV (week), Trakt Trending Movies/Shows            |
| Popular            | TMDB Popular Movies/TV, Trakt Popular Movies/Shows                                                              |
| Streaming Services | Netflix Movies/Shows, Disney+ Movies/Shows, HBO Shows, Amazon Prime Movies/Shows, Hulu Shows (community MDBList) |
| Top Rated          | TMDB Top Rated Movies/TV, IMDb Top 250 Movies, IMDb Top 250 Shows (MDBList)                                     |
| In Theaters        | TMDB Now Playing                                                                                                |
| Upcoming           | TMDB Upcoming Movies                                                                                            |
| On Air             | TMDB Airing Today, TMDB On The Air This Week                                                                    |
| Editorial          | Trakt Recommended Movies/Shows (per profile), Mindfuck Movies, Top Documentary Movies, Top Horror Movies (MDBList) |
| Custom             | MDBList — bring your own JSON URL                                                                               |

Templates default to 100 items. Finite canonical lists override that so the
collection can hold what the title promises: the IMDb Top 250 templates use
250, and catalog lists (Criterion Collection, A24) ship with no limit at all
so they match every owned title. Every template ships with a conservative sync cadence — every
6 hours for trending, daily for popular and streaming services, weekly for
top-rated and editorial picks — and a "featured" hint where appropriate. All
defaults are editable in the small confirmation drawer before the collection
is created.

## Poster artwork

Built-in templates ship with poster artwork under
`web/public/images/collection-templates/`. Poster filenames match template
IDs, for example `tmdb_popular_movies.jpg`; raw generated plates live in
`web/public/images/collection-templates/raw/` so typography can be regenerated
without re-running image generation.

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

Generate the raw plate with Codex's `imagegen` skill / built-in image tool,
then add deterministic typography locally:

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

### Trakt "Recommended" templates

The two Trakt Recommended templates require a profile that already has a
Trakt account connected via **Settings → Watch Providers**. The drawer asks
for a profile picker before submitting; the resulting collection is scoped to
that profile's recommendations and re-syncs daily by default.

## How a template becomes a collection

Picking a template opens a small confirmation drawer with:

- **Library** — which library the collection belongs to.
- **Title / Description** — pre-filled from the template, fully editable.
- **Max Items** — pre-filled with the template's default limit.
- **Sync Schedule** — pre-filled with the template's recommended cadence; can
  be changed to any preset or a custom cron expression.
- **Featured** — surface the collection in hero shelves on the library tab.
- **MDBList URL** — only shown when the template's source is MDBList.
- **Profile** — only shown when the template requires one (Trakt
  Recommended).

Submitting the form posts to the matching import endpoint. The backend
creates the collection, runs the first sync, and the new shelf shows up in
the collections list with its status set by the initial sync run.

## Extending the registry

The catalog lives in
[`internal/collections/templates/builtin.go`](../../../internal/collections/templates/builtin.go),
fronted by a thread-safe registry in
[`internal/collections/templates/registry.go`](../../../internal/collections/templates/registry.go).

To add a template:

1. Append a `Template` struct to `builtinTemplates` (or call
   `templates.Register(...)` from your own startup code if you maintain a
   private fork).
2. Run `go test ./internal/collections/templates/...` — the registry runs
   `validate(...)` on every entry at registration time, so invalid presets,
   unknown media types, or non-HTTP MDBList URLs panic immediately and the
   tests catch them.
3. Restart the server. The `/admin/collections/templates` endpoint will pick
   up the new template without a frontend rebuild — the gallery renders
   straight from the server-supplied catalog.

### Source contracts

| Source | Required fields                                    | Notes                                                     |
| ------ | -------------------------------------------------- | --------------------------------------------------------- |
| `tmdb` | `preset`, `media_type`; `time_window` for trending | Same shape the existing TMDB import endpoint accepts.     |
| `trakt`| `preset`, `media_type`; profile required if `recommended` | Set `requires_profile: true` for `recommended` presets. |
| `mdblist` | `url` (optional — empty means "ask the operator") | Empty URL renders an MDBList URL field in the drawer.   |

### A note on MDBList catalogs

The shipped MDBList templates point at top-ranked public lists from
[mdblist.com/toplists](https://mdblist.com/toplists/). They are
community-maintained, so the upstream URLs may eventually be retired. When
that happens the import job logs a warning during sync; the affected
template can then be retired or re-pointed in `builtin.go`. Operators who
maintain their own MDBList lists can register additional templates against
`templates.Default` at startup without forking this repo.

## API surface

`GET /admin/collections/templates` returns the catalog as
`{ "categories": [{ "category", "label", "templates": [...] }] }`. The shape
mirrors the Go types in `internal/collections/templates/templates.go`. The
admin role is required (the endpoint sits inside the same `/admin/...`
chain as the rest of the collection admin handlers).

## Testing

- `go test ./internal/collections/templates/...` — registry validation, the
  built-in catalog, and category/lookup behaviour.
- `go test ./internal/api/handlers/ -run TestCollectionTemplateHandler` —
  HTTP handler smoke tests.
- `pnpm vitest run src/components/CollectionTemplateGallery
  src/lib/collectionTemplates.test.ts` — frontend filter/search and
  source-dispatch tests.
