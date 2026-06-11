# Manga metadata plugin + series enrichment — design

Status: design (approved in brainstorming). Commands assume the repository root is the cwd.

This is **Sub-project 2** of manga support. Sub-project 1 (the `manga` library type:
folder-derived series, `manga_chapters`, browse/detail) is done and deployed.
After it, every series is a `type='manga'` item with a clean title but **no
cover/synopsis** — nothing enriches it. This spec adds that enrichment.

## Problem

- `type='manga'` series items have no metadata path. The existing ebook
  `Enricher` (`internal/ebooks/enrichment.go`) claims `type='ebook'` only, and it
  searches book databases (Gutenberg, OpenLibrary, …) that do not index manga.
- Manga needs a manga-native source. Series should show cover art, synopsis,
  status, genres, and author/artist.

## Decisions (from brainstorming)

- **Source: AniList** GraphQL (`https://graphql.anilist.co`) — no API key, rich
  structured data, strong coverage of the well-known series scene releases cover.
- **A new dedicated plugin** (`silo-plugin-manga-metadata`), not an addition to
  the book-focused `silo-plugin-ebook-metadata`.
- **High-confidence matching only**: enrich a series only when the search yields
  a clear title match; otherwise leave it title-only (correctness over coverage).
  Store the AniList id so a manual override is possible later.

## Component 1 — the plugin (`silo-plugin-manga-metadata`)

A new sibling repo scaffolded from `silo-plugin-ebook-metadata` (a clean
`metadata_provider.v1` template).

- **Runtime server**: `GetManifest`, `Configure`, `Search`, `GetMetadata`.
  Capability id `manga-metadata`; `default_priority { manga: 1 }` so it is the
  priority provider at content level `manga`.
- **AniList source**: a `Media(search: $q, type: MANGA)` GraphQL query returning,
  per candidate: `id`, `title { romaji english native }`, `coverImage { extraLarge large }`,
  `description`, `status` (`RELEASING`/`FINISHED`/`HIATUS`/`CANCELLED`/`NOT_YET_RELEASED`),
  `genres`, `staff` (edges with role → Story = author, Art = artist), `startDate.year`,
  `format` (`MANGA`/`MANHWA`/`MANHUA`/`ONE_SHOT`/`NOVEL`), `siteUrl`.
- **High-confidence matching lives in the plugin.** `Search(query=series name)`:
  1. Normalize the query (lower, strip punctuation/whitespace).
  2. Normalize and compare against each candidate's romaji/english/native title.
  3. Return a match **only** when exactly one candidate is a clear winner
     (exact normalized match, or near-exact within a tight edit-distance, and a
     manga-type `format`). Ties / fuzzy-only / no candidates → return no results.
  - Excludes `NOVEL` format (those are light novels, not manga).
- **GetMetadata(id=`anilist:<id>`)**: fetch the full record and map to the SDK
  metadata item — cover URL, synopsis (HTML/markdown stripped), status (normalized
  to a Silo status string), genres, author/artist people, canonical title, and the
  `anilist` provider id.
- **Config**: optional `preferred_title_language` (`romaji` default | `english`)
  for the displayed title. No API key.
- **Rate/Errors**: respect AniList rate-limit headers; surface transient network/
  429 errors as retryable (the host treats them as transient), and a clean
  "no confident match" as an empty result (not an error).

## Component 2 — the host manga-series enricher

A new `MangaEnricher` in `internal/manga/enrichment.go`, mirroring the ebook
`Enricher`.

- **Claim** (`manga_enrichment_state` table for back-off, mirroring
  `ebook_enrichment_state`): batch-claim series needing metadata —
  `WHERE mi.type='manga' AND (poster_path IS NULL OR poster_path='') AND last_refreshed IS NULL AND COALESCE(failures,0) < cap`,
  ordered by failures then created_at.
- **Resolve chain**: resolve the provider chain for the series' library folder at
  content level `manga`. The `providerSupportsLevel` logic (audiobook-storm fix)
  only includes providers that *declare* `manga` support, so the book plugin
  (`ebook` only) is automatically excluded — no global-fallback storm.
- **Query + persist**: call the resolved provider(s) with the series **title**;
  on a high-confidence match, persist via the shared image cacher and metadata
  update: cover (`poster_path` + thumbhash), synopsis, status, genres,
  author/artist people, `anilist:<id>` provider id; stamp `last_refreshed`.
- **No-match / failure**: record an attempt in `manga_enrichment_state`. After the
  failure cap, stop re-querying (a permanently-unknown series shouldn't be retried
  forever). Transient errors (network/429) do NOT count toward the cap.
- **Trigger**: a `sync_manga_metadata` task (mirroring `sync_ebook_metadata`) on
  the periodic schedule and kicked after a manga scan completes, plus the worker
  `Run` loop.

## Plugin install + default chain wiring

The manga-metadata plugin is the **default-enabled** metadata source for manga
libraries — no manual admin step required.

- The plugin is built and installed like the ebook plugin (host plugin install
  flow). Because it declares `manga` priority, a manga library's `manga`-level
  chain resolves to it; `providerSupportsLevel` keeps non-manga providers out.
- **`seedDefaultChain` (host) is updated** so that for a `manga` library, the
  `manga`-level chain entry for the `manga-metadata` capability is created
  **enabled by default** (any other providers present at that level — tmdb/tvdb/
  ebook-metadata — stay disabled), mirroring how `ebook-metadata` is the
  default-enabled provider for ebook libraries.
- **Existing manga libraries**: installing the plugin enables it in their already
  -seeded `manga`-level chains (auto-enable on install for the `manga` content
  level), so libraries created before the plugin existed pick it up without a
  manual step.

## Data flow

```
manga scan → type='manga' series (no poster)
   → MangaEnricher claims series (type='manga', no poster, not refreshed)
   → resolve `manga` chain → silo-plugin-manga-metadata (only manga-declaring provider)
   → plugin AniList Search(series title) → high-confidence match (or none)
   → match:    persist cover/synopsis/status/genres/author + anilist id; stamp refreshed
   → no match: record attempt; back off; series stays title-only
```

## Error handling

- No chain / no provider configured for the folder → skip (not a failure).
- AniList network error or HTTP 429 → transient: retry on a later sweep, do not
  increment the permanent-failure counter.
- Confident no-match → increment the back-off counter; capped so it stops.
- HTML/markdown in AniList descriptions → stripped before persisting.

## Testing

- **Plugin**: the AniList source + the high-confidence matcher, against recorded
  AniList JSON fixtures — exact match, near-exact (punctuation/case), ambiguous →
  no-match, `NOVEL`/format exclusion, multi-candidate tie → no-match, empty result.
  GraphQL request building is pure and unit-tested; HTTP is the one boundary.
- **Host**: the claim query and persist follow the codebase's thin-DB pattern
  (validated by live re-scan); any pure mapping (provider result → `MetadataUpdate`,
  AniList status → Silo status) is unit-tested. The manga-support exclusion of the
  book plugin at content level `manga` is covered by the existing
  `providerSupportsLevel` tests.

## Out of scope

- A manual operator review queue for ambiguous matches (high-confidence-only was
  chosen; manual override is enabled later via the stored `anilist` id).
- Multi-source fallback (MangaDex etc.) — AniList only for this cut.
- Android/iOS client rendering of the new series metadata.
- Per-chapter metadata (chapters are never individually enriched — already
  excluded from ebook enrichment in Sub-project 1's follow-up fix).

## Dependencies

- Sub-project 1 (the `manga` library type) — done/deployed.
- The `providerSupportsLevel` chain logic (audiobook-storm fix) — in `main`.
- The shared image cacher (`metadata.ImageCacher`) used by the ebook enricher.
