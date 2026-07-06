---
title: Supported Media Folder Structures and Naming
description: Accurate reference for the folder layouts and filename patterns Silo supports today.
summary: Supported movie and series organization rules, naming conventions, and known ambiguous cases.
tags:
  - silo
  - docs
  - wiki
  - libraries
  - scanner
  - metadata
audience:
  - operator
  - end-user
last_reviewed: 2026-04-11
related:
  - ../index.md
---

# Supported Media Folder Structures and Naming

This page documents the folder layouts and filename conventions that Silo supports today.
It is based on the current scanner, naming, and metadata-matching code paths, plus a validation pass
against the dev anime library on `2026-04-11`.

All examples in this page use generic placeholder names rather than real media titles.

## Core Rules

- Movies and series do not have the same requirements.
- Series are expected to be contained within a single parent show folder.
- Movies can be stored either in a dedicated movie folder or as loose files.
- Mixed libraries are supported, but they rely on heuristics and are more likely to become ambiguous
  when names and folders are inconsistent.
- Provider ID tags such as `{tvdb-12345}`, `{tmdb-12345}`, or `{imdb-tt1234567}` are strongly
  recommended because they improve matching and reduce ambiguity.

## Supported Series Layouts

### Recommended series layout

```text
/television/Show Name (2024) {tvdb-12345}/Season 01/Show Name - S01E01 - Pilot.mkv
```

This is the clearest and most reliable structure for series.

### Also supported

Series content is supported in these layouts:

- Show folder with `Season XX` directories.
- Show folder with `Season XX - extra text` directories, such as arc names.
- Show folder with numeric season directories like `01` when the surrounding context still looks
  like series content.
- Show folder with `Specials` directories.
- Show folder with `Extras` directories. Silo maps these to season `0`.
- Show folder with episode files directly under the show folder when the filenames contain a
  supported episodic token such as `S01E03`.

Examples:

```text
/tv/Series Name (2008)/Season 01/Series.Name.S01E01.mkv
/tv/Show Name/Specials/Show.Name.S00E01.mkv
/tv/Show Name/Extras/Show.Name.S00E01.mkv
/mixed/Show Name/01/Show Name S01E03.mkv
/mixed/Show Name/Show Name S01E03.mkv
```

One narrow edge case is worth calling out separately:

```text
/television/anime/Series Name/Season 01 - Arc 01 - Arc Name/Series.Name.E01.1080p.mkv
```

That kind of path can still help Silo recognize the show root because the folder layout is
strong, but bare `E01` is not a generally supported episode parsing format.

## Supported Episode Filename Patterns

Silo currently treats `SxxExx` as the supported episode token.

Supported examples:

```text
Show Name - S01E01 - Pilot.mkv
Show Name.S01E03.mkv
Show Name - S00E01 - Special.mkv
Show Name - S01E01.001 - Pilot [Bluray-1080p][x265].mkv
```

Important details:

- The parser only needs the `SxxExx` part. Extra text after it is tolerated.
- Decimal suffixes such as `S01E01.001` are supported because the parser still recognizes the
  leading `S01E01`.
- Release metadata and noisy suffixes after the episode token are tolerated as long as the file
  still contains a valid `SxxExx` token.
- `Specials` and `Extras` use season `0`, so `S00E01` is supported.

Representative validated pattern from the dev server:

```text
Series Name (2017) - S01E01.001 - Episode Title [Bluray-1080p][10bit][x265][AC3 5.1][EN+JA]-GROUP][.mkv
```

## Series Patterns That Are Not Reliably Supported

These patterns should be treated as unsupported or ambiguous:

- Unrelated episodes from different series thrown into the same folder.
- Episode files that use only absolute numbering, such as `Stone Ocean 24.mkv`, without `SxxExx`.
- Bare `E01` filenames as a general naming convention.
  Silo has a narrow test case where `E01` still helps identify a season-folder layout, but it
  is not a generally supported episode parsing format.
- Series files with no clear show folder, no supported episode token, and no provider-tagged parent
  folder.

If a series library contains a flat dump of unrelated episodes, Silo can collapse them into
the same inferred root because the current model assumes one parent show folder per series.

When Silo cannot confidently classify or reconcile a root, it can surface the content as an
explicitly ambiguous item instead of auto-matching it. In practice, unsupported layouts often show
up as ambiguous or pending items rather than as clean matches.

## Supported Movie Layouts

Movies are more flexible than series.

### Recommended movie layout

```text
/movies/Movie Name (2016)/Movie.Name.2016.1080p.BluRay.mkv
```

### Also supported

- Loose movie files in a movie library.
- Dedicated movie folders with provider tags.
- Release-style filenames inside a trusted movie folder.

Examples:

```text
/movies/Movie Name (2016)/Movie.Name.2016.1080p.BluRay.mkv
/movies/Movie Name [imdbid-tt1234567]/Movie Name.mp4
/movies/Loose Movie.2024.2160p.WEB-DL.mkv
/movies/Movie Name {imdb-tt1234567} {tmdb-12345}/Movie.Name (2023) [Remux-1080p].mkv
```

Silo can infer a synthetic root for a truly loose movie file when there is no trusted movie
folder around it.

## Supported Mixed-Library Behavior

Mixed libraries are supported, but they are heuristic-driven.

Silo resolves mixed-library content roughly like this:

- If the path has season structure, it is treated as series.
- If the enclosing folder looks like a movie folder, it is treated as a movie.
- If the filename contains `SxxExx`, it is treated as series.
- Otherwise it falls back to movie.

This means mixed libraries can work well, but they are less deterministic than separate movie and
series libraries.

## Provider Tags

Provider tags are supported in folder names and are strongly recommended.

Supported formats include:

```text
Series Name (2024) {tvdb-12345}
Movie Name (2024) {tmdb-12345}
Movie Name [imdbid-tt1234567]
```

Benefits of provider tags:

- More reliable initial skeleton creation.
- Better deduplication.
- Less ambiguity in mixed libraries.
- Better resilience when release filenames diverge from the human title.

## Sidecar Files and Supplemental Files

Silo often sees sidecar files that mirror the media basename:

- `.nfo`
- `.bif`
- `-thumb.jpg`
- `tvshow.nfo`
- `season.nfo`
- posters, banners, logos, and fanart

These are common and expected. Episode naming guidance in this page refers to the media files
themselves, but sidecars may legitimately reuse the same stem.

Supplemental directories next to a movie (and directly under a series root)
are scanned as **extras** attached to that item, following the Jellyfin/Plex
folder convention:

- `Trailers`, `Teasers`
- `Featurettes`
- `Behind the Scenes`
- `Deleted Scenes`
- `Clips`, `Shorts`, `Interviews`, `Scenes`
- `Extras`, `Other`

Filename suffixes on files sitting next to the movie are also recognized:
`Movie (2020)-trailer.mkv`, `-teaser`, `-featurette`, `-clip`,
`-behindthescenes`, `-deleted`, `-interview`, `-short`, `-other`.

Extras never appear as versions of the main title; they show in the item's
Extras section and play like any other file. Extras are bound to the item
owning the surrounding folder, so an extras directory at the library root is
ignored. For series libraries, `Extras/` files carrying a valid `SxxExx`
token keep their documented season-`0` mapping and are not treated as extras.

Noise content is still intentionally skipped:

- `Sample` / `Samples` directories and `Sample.mkv`-style files
- `Subs` / `Subtitles` directories (handled by subtitle detection)

## What The Dev Anime Library Validated

A large anime series library on the dev server was audited to validate that this page reflects
real-world usage and not just unit tests.

Observed on `2026-04-11`:

- `4038` top-level show directories.
- `4029` show directories with at least one `Season XX` directory.
- `1184` show directories with a `Specials` directory.
- `4036` show directories with a provider tag in the folder name.
- `474304` files matching `SxxExx.xxx`.
- `87289` `.mkv` files matching `SxxExx.xxx`.

That makes `SxxExx.xxx` an important supported pattern, not a corner case.

## Recommended Practices

- Put every series inside its own parent show folder.
- Use `Season XX` and `Specials` when possible.
- Include the year in the top-level folder when it is known.
- Add provider tags when you can.
- Keep movies and series in separate libraries unless you specifically want mixed-library
  heuristics.
- Do not rely on flat folders of unrelated episodes.

## Source References

- `internal/naming/filename.go`
- `internal/naming/root_inference.go`
- `internal/scanner/scanner.go`
- `internal/metadata/service.go`
- `internal/naming/filename_test.go`
- `internal/scanner/root_observation_test.go`
- `internal/metadata/service_test.go`
