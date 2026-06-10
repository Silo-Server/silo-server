# Manga library type — design (host)

Status: design (approved in brainstorming). Commands assume the repository root is the cwd.

## Problem

A "Manga" library (currently library type `ebooks`) scans ~32,700 `.cbz`/`.cbr`
archives and matches almost nothing (≈406 matched of ~32,400). Three compounding
causes:

1. **No structured metadata in the files.** Sampled archives contain no
   `ComicInfo.xml` (they are scene/digital releases). `parseEbookCBZ`
   (`internal/scanner/ebook.go`) therefore extracts only a cover image + page
   count — no title, series, or volume.
2. **No grouping.** With an empty parsed title, the scanner falls back to the
   filename (`ebookTitleFromPath`), and `ebookContentGroupKey` keys identity on
   that filename. Every chapter (`One-Punch Man 178 …`, `… 179 …`) gets a
   distinct group key, so chapters never collapse into a series. `ebook_series`
   is a flat facet `(content_id, series_name, series_index)` — there is no
   series entity, and it is never populated for these files.
3. **The query is junk.** The metadata provider receives the whole filename as
   the search query (`Query: query.Title` in `internal/metadata/plugin_provider.go`),
   e.g. `Heavenly Delusion 061 (2024) (Digital) (LuCaZ)` — unmatchable anywhere.

The folder tree *does* encode the series:
`…/manga/.../Kurosagi Corpse Delivery Service/V2006/… #10.cbz` — the parent
directory is the series name, far more reliable than the filename.

## Goal

A manga library where each series (e.g. "One-Punch Man") is **one rich entry** —
cover, synopsis, author/artist, status, genres — with its volumes/chapters
grouped underneath and individually readable.

This requires two sub-projects:

- **Sub-project 1 (this spec, host):** a `manga` library type that detects
  series from the folder tree, groups chapters under a first-class series item,
  parses volume/chapter ordering, and exposes a clean series name for
  enrichment.
- **Sub-project 2 (separate spec, plugin `silo-plugin-ebook-metadata`):** a
  manga-native metadata source (AniList/MangaDex) that enriches the series item.

Host-first: without the series structure there is nothing for rich metadata to
attach to, and the metadata query stays junk.

## Scope

In scope (host): the `manga` library type, the series/chapter data model, the
manga scan path, catalog browse/detail wiring, the enrichment hand-off, and a
backfill that converts the existing library.

Out of scope: the manga metadata source itself (sub-project 2), and any change
to TV/movie/audiobook/flat-ebook behavior.

## Design

### Library type: `manga`, forked from `ebooks`

`manga` is a new library type that **reuses the entire ebook base** — the
`.cbz`/`.cbr` reader, cover extraction, `parsedEbook` parsing, `ebook`-typed
readable chapter items, and progress tracking. The only added behavior is series
detection + grouping during scan and series-card presentation in the catalog.

A manga library scans through a new manga path; an ebook library is unchanged.
Library 8 is converted `ebooks → manga` (see Backfill).

### Data model

- **Series:** a `media_item` with `type='manga'` — one per detected series
  folder. Holds the rich series metadata (cover, synopsis, author/artist,
  status, genres). It is the browse card and the enrichment target. Analogous to
  a TV `series` item.
- **Chapter:** each archive stays a `media_item` with `type='ebook'` — unchanged
  and readable. Linked to its series via a new table.
- **Link table `manga_chapters`** (mirrors `episodes`):

  ```sql
  CREATE TABLE manga_chapters (
      chapter_content_id TEXT PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
      series_content_id  TEXT NOT NULL    REFERENCES media_items(content_id) ON DELETE CASCADE,
      chapter_index      NUMERIC,         -- ordering key (volume or chapter number)
      volume             TEXT,            -- raw volume token when present (e.g. "v01", "V2006")
      updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
  );
  CREATE INDEX manga_chapters_series ON manga_chapters (series_content_id, chapter_index NULLS LAST);
  ```

  The series item's `content_id` is the join target — chapters attach to a real
  entity (unlike `ebook_series`, which keys only on a `series_name` string).

### Scanner (manga path)

Reuse the ebook scanner's per-file parsing (`parseEbookFile` → cover, page
count). Add, per archive:

1. **Series folder detection.** Walk up from the file's directory; the **series
   folder** is the nearest ancestor whose name is *not* a volume marker (matches
   `^[vV](ol\.?)?\s*\d+` and similar, e.g. `v01`, `V2006`, `Vol 3`). Its name,
   normalized, is the series name. One `manga` series item per (library, series
   folder).
2. **Index parsing** (`manga` filename/folder parser, the riskiest unit — see
   Parsing rules). Produce `chapter_index` (NUMERIC) and `volume` (raw token)
   from the filename, stripping `(year) (Digital) (group)` noise.
3. **Series item upsert.** Find-or-create the `type='manga'` item for the series
   folder; set its `title` to the series name; mark it as needing enrichment.
4. **Chapter item upsert.** Create/keep the `ebook` chapter item (as today), then
   upsert its `manga_chapters` row (series link + index + volume).

Series-item identity uses a content group key derived from `(library_id, series
folder)` so re-scans are idempotent and additional chapters attach to the
existing series.

### Parsing rules (folder + filename)

Series name comes from the **folder** (preferred); the filename is the fallback.
The **index** comes from the filename. Patterns observed, in priority order:

| Pattern | Example | volume | chapter_index |
|---|---|---|---|
| `… v<NN> …` | `Bakuman v13 (2012) …` | `v13` | 13 |
| `… c<NNN> …` | `Dead Mount Death Play c128 …` | — | 128 |
| `… Vol.<YYYY> #<NN> …` | `Berserk Vol.2003 #04 …` | `Vol.2003` | 4 |
| bare number | `One-Punch Man 178 (2023) …` | — | 178 |
| decimal | `Kindergarten WARS 109.1 …` | — | 109.1 |

Noise stripped before parsing: trailing `(####)`, `(Digital)`, `(<group>)`,
`(Month, Year)`. The parser is pure (no I/O) and unit-tested against a corpus of
real filenames drawn from the live library.

Ambiguity handling: when both a volume token and a bare number exist, the
volume token wins for `volume`; `chapter_index` is the most specific number
found. When nothing parses, `chapter_index` is NULL (chapters sort last,
stable by title) — the series still forms.

### Catalog, browse, detail, reader

- **Browse:** `type='manga'` renders as **series cards** (like TV `series`),
  not per-chapter. The flat-ebook browse path is untouched.
- **Series detail:** lists the series' chapters from `manga_chapters` ordered by
  `chapter_index NULLS LAST, sort_title`.
- **Reader:** chapters are `ebook` items and open in the existing `.cbz` reader
  unchanged; progress tracking is unchanged.
- **Enrichment hand-off:** the provider chain for a manga library resolves at a
  new content level `manga`; the metadata query is the **series item's title**
  (the clean folder-derived series name). Sub-project 2's plugin enriches the
  series item. Chapters are not individually enriched.

### Backfill / migration

- A migration adds `type='manga'` to allowed item/library types and creates
  `manga_chapters`.
- An admin action (or migration step) converts library 8 `ebooks → manga`.
- A re-scan of the converted library rebuilds the existing flat `ebook` items
  into manga series + linked chapters: series items are created, chapter items
  are linked, and orphaned flat items that no longer represent a standalone book
  are reconciled. The re-scan is idempotent and safe to re-run.

## Testing

- **Unit (highest value):** the manga filename/folder parser and series-folder
  detection, against a corpus of real names from the live library (volumes,
  chapters, `Vol.YYYY #NN`, decimals, noise, and edge cases). This is the
  heuristic-heavy, regression-prone part.
- **Scanner integration:** a fixture tree (`Series/Vol/…cbz`, `Series/…cbz`)
  produces exactly one series item per series with chapters linked and ordered.
- **Idempotency:** re-scanning the fixture adds no duplicates and re-attaches
  new chapters to the existing series.

## Risks / open questions

- **Series-folder heuristic** may misfire on flat layouts (`Series/Series 178.cbz`
  with no volume subdir) or deep/curated trees (`manga/MangaOfficial/CLEAN/…`).
  Mitigation: drive series name from the nearest non-volume folder and validate
  against the live tree corpus; allow a manual series override later (reuse the
  existing group-override mechanism).
- **Mixed volume + chapter numbering** within one series (some files `v01`,
  others `c128`) can order oddly. Mitigation: `chapter_index` is a single
  NUMERIC; document that volumes and loose chapters interleave by number, and
  revisit if real libraries need separate volume/chapter axes.
- **Backfill churn** on ~32k items; must be idempotent and resumable.

## Hand-off to sub-project 2 (plugin)

After this lands, a manga series item exists per series with a clean
folder-derived title, requesting enrichment at content level `manga`. Sub-project
2 adds a manga-native source to `silo-plugin-ebook-metadata` so that title
resolves to rich series metadata (cover, synopsis, author/artist, status,
genres), written back to the series item.
