---
title: Local NFO Metadata
description: How Silo reads Kodi/Jellyfin-style NFO sidecar files and merges them with online metadata providers.
summary: Supported NFO fields, merge and refresh semantics, and the naming-supplies-structure contract for curated libraries.
tags:
  - silo
  - docs
  - wiki
  - libraries
  - metadata
  - nfo
audience:
  - operator
  - end-user
last_reviewed: 2026-07-12
related:
  - ../index.md
  - media-folder-and-naming.md
---

# Local NFO Metadata

Silo ships a built-in **NFO Files** metadata provider that reads Kodi/Jellyfin-style `.nfo`
sidecar files. It runs inside the same provider chain as online providers (TMDB, TVDB, ...),
so one toggle and one priority per library control it. By default it sits at priority 1: the
NFO wins every field it declares, and online providers fill in whatever it leaves out.

Clients can detect whether a library type includes the NFO provider via
`GET /api/v1/libraries/provider-defaults`.

## Where Silo looks for NFO files

For each movie or series, Silo checks (in order):

1. `movie.nfo` / `tvshow.nfo` in the item's folder
2. `<media basename>.nfo` next to the media file (e.g. `My Movie (2021).nfo`)

For series, Silo additionally reads:

- `season.nfo` inside each season directory (`<season>` root: title, plot)
- `<episode basename>.nfo` next to each episode file (`<episodedetails>` root: title,
  plot, air date, runtime, ratings)
- Season posters: `poster.jpg` (or `folder`/`cover`) inside the season directory, or
  `seasonNN-poster.jpg` in the series root
- Episode thumbnails: `<episode basename>-thumb.jpg`

A sidecar only applies when its root element matches the item type: a stray `movie.nfo` in
a series folder is skipped in favor of `tvshow.nfo`, and a `tvshow.nfo` next to a movie
file is ignored. Files that fail to parse are skipped silently and the next candidate is
tried — a broken NFO never blocks matching.

Season and episode numbers declared inside an NFO (`<seasonnumber>`, `<season>`,
`<episode>`) are advisory: the directory name and the `SxxEyy` filename pattern decide
where metadata lands, and a mismatch is logged and resolved in the filename's favor.
Multi-episode NFO documents (several `<episodedetails>` blocks in one file) are not
supported; the first block is used and a warning is logged.

## Supported fields

| NFO element | Applies to | Silo field |
|---|---|---|
| `<title>` | movie, series | Title |
| `<originaltitle>` | movie, series | Original title |
| `<tagline>` | movie, series | Tagline |
| `<plot>` | movie, series | Overview |
| `<year>` | movie, series | Year (derived from the premiere date when omitted) |
| `<runtime>` | movie, series | Runtime in minutes (non-numeric values ignored) |
| `<premiered>` / `<releasedate>` | movie | Release date (`YYYY-MM-DD`) |
| `<premiered>` / `<aired>` | series | First air date (`YYYY-MM-DD`) |
| `<mpaa>` | movie, series | Content rating |
| `<genre>` (repeated) | movie, series | Genres |
| `<studio>` (repeated) | movie, series | Studios |
| `<country>` (repeated) | movie, series | Countries |
| `<tag>` (repeated) | movie, series | Keywords |
| `<ratings>` / `<rating>` | movie, series | Ratings (named sources: `imdb`, `tmdb`/`themoviedb`, `tomatometerallcritics`, `tomatometerallaudience`; a legacy bare `<rating>` fills the IMDB slot) |
| `<actor>` (`name`/`role`/`order`) | movie, series | Cast (actor `<thumb>` URLs are ignored) |
| `<director>`, `<credits>` | movie, series | Crew (directors and writers) |
| `<uniqueid type="tmdb|imdb|tvdb">` | movie, series | Trusted identity anchor for matching |
| `<title>`, `<plot>` (`season.nfo`) | season | Season name and overview |
| `<title>`, `<plot>`, `<aired>`, `<runtime>`, `<ratings>` (`<episodedetails>`) | episode | Episode title, overview, air date, runtime, ratings |

Not yet supported: `<set>` (collections) — a planned follow-up. `<userrating>` is read
but not stored (Silo has no per-user rating field). Unknown elements are ignored, so
exports from Kodi, Jellyfin, or tinyMediaManager work as-is.

## How NFO data merges with online providers

- **NFO wins what it declares.** With the NFO provider at priority 1, every field the NFO
  populates beats the same field from online providers.
- **Online providers backfill the rest.** Anything the NFO leaves out (cast, artwork,
  runtime, ratings, ...) is filled by the next providers in the chain, keyed by the
  `<uniqueid>` values when present.
- **Genres are all-or-nothing.** The first provider that supplies any genres supplies the
  whole list; later providers do not append to it. An NFO with no `<genre>` entries leaves
  genres entirely to online providers.
- **Field locks are respected.** Fields locked in the Edit Metadata dialog (including
  Year and release/air dates) are never overwritten by any provider, NFO included.

## Editing an NFO after the initial match

Scheduled background refreshes only fill empty fields — they intentionally do **not**
re-apply NFO changes over existing values. To pick up edits:

**Edit the NFO, then use Refresh metadata on the item.** A manual refresh re-reads the
sidecar and replaces all unlocked fields with the merged provider results, NFO first.

## Naming supplies structure, NFO supplies presentation

NFO files describe *what an item is called and about* — they do not change *how files are
grouped*. Folder layout and filenames still decide what is a series, a season, and an
episode (see [Supported Media Folder Structures and Naming](media-folder-and-naming.md)).
This makes fully local libraries with no online match work well, for example a fitness
library:

```
Fitness/
  P90X/
    tvshow.nfo            # show title/plot (no <uniqueid> needed)
    poster.jpg fanart.jpg
    Season 01/
      season.nfo          # "Course A: Classic"
      poster.jpg          # season poster
      P90X S01E01 - Chest and Back.mkv
      P90X S01E01 - Chest and Back.nfo        # episode title/plot
      P90X S01E01 - Chest and Back-thumb.jpg  # episode thumbnail
```

The folder and episode naming build the series/season/episode tree; the NFO sidecars
supply every name, description, and image — even when no online provider knows the
content. Episodes without an `.nfo` still appear with their generic "Episode N" title,
so partially curated libraries degrade gracefully.

## Mixed libraries (sports example)

A single library of type **Mixed** can hold movie-shaped and show-shaped content side by
side — the common case is sports, where pay-per-view events are movies and the weekly
show is a series, and most of it has no TMDB/TVDB presence:

```
WWE/
  WrestleMania 41 (2025)/
    WrestleMania 41 (2025).mkv
    movie.nfo             # event title/plot (a <uniqueid> is optional)
    poster.jpg
  WWE SmackDown/
    tvshow.nfo
    Season 27/
      season.nfo
      poster.jpg
      WWE SmackDown S27E15.mkv
      WWE SmackDown S27E15.nfo
      WWE SmackDown S27E15-thumb.jpg
```

How classification works in a mixed library:

- **Naming decides the type, per file, before any metadata provider runs.** An `SxxEyy`
  pattern or a `Season NN` folder routes a file to the series lane; everything else is a
  movie. An NFO never flips the type: a `tvshow.nfo` sitting next to a movie-classified
  file is ignored (the root element must match the item type), not applied.
- **NFOs supply metadata and identity only.** Once the type is fixed, the NFO provider
  behaves exactly as in dedicated movie/series libraries — including season and episode
  depth for the show-shaped content. An event whose `movie.nfo` carries a
  `<uniqueid type="tmdb">` still gets full online enrichment, while ID-less events next
  to it fall back to their local NFO and artwork.
- **Misclassified group?** Override its **Type** on the root in the library admin
  (Auto → Movie/Series) and rescan — that is the correction path; fixing the NFO alone
  will not re-route a file.

Tools that generate sports libraries (sportarr-style managers) should therefore write
standard `SxxEyy` naming for show content and standalone movie naming (`Title (Year)/`)
for events, and place NFOs/posters exactly as in the dedicated-library layouts above.

## Source References

- `internal/metadata/nfo/` — parser and provider
- `internal/metadata/merge.go` — merge semantics and field locks
