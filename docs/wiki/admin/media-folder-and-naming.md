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
last_reviewed: 2026-09-22
related:
  - ../index.md
---

# Supported Media Folder Structures and Naming

Silo reads the title, year, episode numbers, and provider IDs from filenames and folders.
You do not need to rename files to one specific template. Common Sonarr/Radarr, FileBot,
and scene release names are supported when they contain enough information to identify
the content. A naming pattern supplies search hints; it cannot guarantee a unique provider match.

This page describes the current code and regression tests. The historical library audit
below covers the narrower set of patterns observed at that time.

Examples use placeholder names except where a real title illustrates significant punctuation.

## Core Rules

- Movies and series do not have the same requirements.
- Series can use a parent show folder or files directly in a configured library
  path containing the show name and episode information.
- Movies can be stored either in a dedicated movie folder or as loose files.
- Mixed libraries are supported, but they rely on heuristics and are more likely to become ambiguous
  when names and folders are inconsistent.
- Provider ID tags such as `{tvdb-12345}`, `{tmdb-12345}`, or `{imdb-tt1234567}` are optional.
  They resolve ambiguity when several titles share a name or when filenames are obscured.

## Supported Series Layouts

### Recommended series layout

```text
/television/Show Name (2024) {tvdb-12345}/Season 01/Show Name - S01E01 - Pilot.mkv
```

This is the clearest and most reliable structure for series.

### Also supported

Series content is supported in these layouts:

- Show folder with `Season XX`, `Season_XX`, `Season.XX`, or `SXX` directories.
- Localized season labels, including `Staffel`, `Stagione`, `Temporada`, `Sæson`,
  `Säsong`, `Seizoen`, `Kausi`, `Sezon`, `시즌`, `シーズン`, and `сезон`;
  reversed labels such as `3.Staffel` also work.
- Season packs such as `Show.Name.S01.COMPLETE`, either as a show root or beneath
  a matching show folder.
- Show folder with `Season XX - extra text` directories, such as arc names.
- Show folder with numeric season directories like `01` when the surrounding context still looks
  like series content.
- Show folder with `Specials` directories.
- Show folder with `Extras` directories. Files with explicit episode coordinates
  remain episodes; other files are attached as extras.
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

## Supported Episode Filename Patterns

Explicit season and episode numbers work with or without a season folder. Compact
codes such as `Show.Name.103` require series context:

```text
Show Name - S01E01 - Pilot.mkv
Show.Name.s01.e03.mkv
Show Name - 1x03 - Episode Title.mkv
Show Name - S01xE03 - Episode Title.mkv
Show.Name.103.mkv
Show Name - 2009x03 - Episode Title.mkv
Show Name Season 1 Episode 3.mkv
Show Name S 01 E 03.mkv
Show Name - S00E01 - Special.mkv
Show Name - S01E01.001 - Pilot [Bluray-1080p][x265].mkv
```

A season folder can supply the season number for episode-only filenames:

```text
/tv/Show Name/Season 02/E03 - Episode Title.mkv
/tv/Show Name/S02/Episode 03.mkv
/tv/Show Name/Season_02/03 - Episode Title.mkv
/tv/Show Name/Season 02/003.Episode.Title.mkv
/tv/Show Name/Specials/E01.mkv
```

In a series library, episode-only names also work without a season folder:
`Show Name/E03.mkv`, `Show Name/03.mkv`, and `[Group] Show Name - 136 [720p].mkv`.
Silo preserves that missing season information. A number alone links only when it
identifies a unique episode in season 1, where absolute and per-season
numbering agree. A distinctive episode title that matches exactly one
episode can link a later season or an absolute number. Ambiguous numbers remain
unlinked; they do not become Specials.
Scanner-generated fallback episodes cannot establish the missing season.

Compact three-digit codes such as `Show.Name.103` mean season 1 episode 3. A
space-separated number such as `Show Name 103` remains an episode number without
an inferred season. Absolute and compact conventions can overlap, so metadata or
explicit season labels are needed when the numbering order is ambiguous.
An explicit season folder takes precedence over a conflicting compact code:
`Season 21/301.mkv` supplies season 21, episode 301.

Episode-only numbers in trailer and extra directories do not acquire episode
coordinates. An explicit season in the filename takes precedence over the folder.

Release details after an episode token are tolerated. Decimal suffixes such as
`S01E01.001` retain the leading `S01E01` coordinate. Explicit episode numbers can
contain up to five digits; longer digit runs do not become truncated episode numbers.

Date-based names such as `Show Name - 2026-04-24 - Episode Title.mkv` are supported
in series context. Dots, underscores, and spaces also work as date separators.
A valid date is linked only when the series metadata identifies an unambiguous episode
for that date.

## Series Identity and Ambiguous Numbering

Files such as `Show.One.S01E01.mkv` and `Show.Two.S01E01.mkv` can share a configured
library path. Silo extracts their show names and gives them separate identities.
Inside a show folder, that folder supplies the show identity: for example,
`Show Name/Pilot - S01E01.mkv` belongs to `Show Name`. Daily files at a library
path also supply show names, such as `Show.Name.2026.04.24.mkv`.

If a nested directory holds several shows without individual show folders, add
that directory as a library path. The configured boundary distinguishes a shared
container from a show folder without guessing from its name.

Ranges and episode lists such as `S01E01-E03`, `S01E01E02`, `1x01x02`, and
`2009x03-E15` preserve their starting and ending episodes. The file links to its
starting episode. Separate catalog association and presentation for every covered
episode remain tracked in [issue #739](https://github.com/Silo-Server/silo-server/issues/739).

These cases still need additional evidence or a manual match:

- Absolute/DVD numbering that disagrees with the provider's episode order and has
  no uniquely corroborating episode title. Silo does not invent a cumulative order
  from incomplete season metadata.
- Disc track names such as `title00.mkv`: track order does not establish episode order.
- Anonymous episode filenames sharing a folder that does not identify their show.
- Conflicting provider IDs or inconsistent folder/file identities.

A rescan updates stored identities after naming rules change. A queued root that
now contains different filename-derived shows is held for a rescan before any bulk
relinking. Manual group overrides remain authoritative.

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
- Release-style filenames, with or without a year.
- Generic or obscured filenames inside a movie folder identified by a year in brackets,
  a provider tag, or a release name containing both a year and release details.

Examples:

```text
/movies/Movie Name (2016)/Movie.Name.2016.1080p.BluRay.mkv
/movies/Movie Name [imdbid-tt1234567]/Movie Name.mp4
/movies/Loose Movie.2024.2160p.WEB-DL.mkv
/movies/Movie.Name.1080p.BluRay.x264-GROUP.mkv
/movies/Movie Name [2024] 720p 6ch.mkv
/movies/Movie Name (2024)/title00.mkv
/movies/Movie.Name.2024.1080p.BluRay/title_t00.mkv
/movies/Movie Name {imdb-tt1234567} {tmdb-12345}/Movie.Name (2023) [Remux-1080p].mkv
```

Dots and underscores can separate title words. Recognized resolution, source, and codec
suffixes are removed from movie search titles, including `1080p`, `BluRay`, `WEB-DL`,
`DVDRip`, `x264`, `XviD`, `UHD`, `4K`, `AV1`, and audio codecs. Recognized release
metadata in brackets is removed while title brackets such as `[REC]` and dotted
acronyms are preserved. Ordinary words such as “Web” and “Extended” alone are
not sufficient to identify a release suffix. Numbers after a technical suffix do
not become release years.

Silo can infer a synthetic root for a truly loose movie file when there is no trusted movie
folder around it. A generic filename such as `title00.mkv` in an unidentified folder
still needs local metadata or a manual match. The parser cannot determine which disc
track contains the feature film from the track number.

## Supported Mixed-Library Behavior

Mixed libraries are supported, but they are heuristic-driven.

Silo resolves mixed-library content roughly like this:

- If the path has season structure, it is treated as series.
- If the enclosing folder looks like a movie folder, it is treated as a movie.
- If the filename contains explicit season and episode numbers, such as `S01E02`
  or `1x02`, it is treated as series.
- Otherwise it falls back to movie.

This means mixed libraries can work well, but they are less deterministic than separate movie and
series libraries.

## Provider Tags

Provider tags are supported in folder and file names. They are optional identity anchors.

Supported formats include:

```text
Series Name (2024) {tvdb-12345}
Series Name [tvdbid=12345]
Movie Name (2024) [tmdbid=12345]
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

Some NFO sidecars are actively read: `movie.nfo`, `tvshow.nfo`, `<media basename>.nfo`,
`season.nfo`, `<episode basename>.nfo`, and sidecar artwork (season posters, episode
thumbnails) feed the built-in NFO metadata provider — including `<uniqueid>` provider IDs,
which act as trusted matching anchors just like `{tmdb-...}` folder tags. NFO files never
change how files are grouped; naming and layout still decide series/season/episode
structure. See [Local NFO Metadata](nfo-local-metadata.md) for the supported fields and
merge behavior.

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
ignored. For series libraries, files under `Extras/` with explicit season and
episode numbers remain episodes. Their filename supplies the season: `S00E01`
is a special, while `S01E01` belongs to season 1.

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

- A separate show folder provides useful context, especially for episode-only filenames.
- Use `Season XX` and `Specials` when possible.
- Include the year in the top-level folder when it is known.
- Add provider tags when you can.
- Keep movies and series in separate libraries unless you specifically want mixed-library
  heuristics.
- For files directly in a configured series library path, include the show name
  in each filename.

## Naming References

Regression cases include examples from:

- [FileBot format expressions](https://www.filebot.net/naming.html)
- [Sonarr naming defaults](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Organizer/NamingConfig.cs)
- [Radarr naming defaults](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Organizer/NamingConfig.cs)
- [TRaSH movie naming recommendations](https://trash-guides.info/Radarr/Radarr-recommended-naming-scheme/)

## Source References

- `internal/naming/episode.go`
- `internal/naming/episode_parity_test.go`
- `internal/naming/season_test.go`
- `internal/naming/movie_parity_test.go`
- `internal/metadata/unseasoned_episode_test.go`
- `internal/naming/flexible_naming_test.go`
- `internal/metadata/flexible_naming_test.go`

- `internal/naming/filename.go`
- `internal/naming/root_inference.go`
- `internal/scanner/scanner.go`
- `internal/metadata/service.go`
- `internal/naming/filename_test.go`
- `internal/scanner/root_observation_test.go`
- `internal/metadata/service_test.go`
