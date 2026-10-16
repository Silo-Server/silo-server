# Local NFO metadata and sidecar artwork (as built)

Implements #216: Kodi/Jellyfin-style `.nfo` files and sidecar images
(`poster.jpg`, `fanart.jpg`, …) next to movies and shows are read by a builtin
metadata provider, so curated local libraries work with partial or zero
TMDB/TVDB coverage. Admin-facing usage lives in the user manual at
https://siloserver.org/docs/local-metadata; this page records the architecture
for maintainers.

## Registration: a builtin provider in the plugin chain

Provider chains are keyed on `plugin_installations` rows, so builtins are
registered **as data**: a reserved installation (`plugin_id='silo.builtin'`,
`kind='builtin'`, sentinel `install_path`, `update_policy='manual'`) plus a
`metadata_provider.v1` capability `nfo` with `default_priority` 1 for
movie/series/season/episode
(`migrations/sql/20260712100125_builtin_host_providers.sql`,
`...124741_nfo_capability_season_episode_levels.sql`). It shipped with
`default_enabled=false`; `...20261011201709_local_metadata_default_on.sql`
turned it on by default, enabled every existing `nfo` chain row and moved the
builtin rows to the front of each chain.

- `internal/metadata/builtin.go` is the in-process registry; providers
  self-register (`internal/metadata/nfo/register.go`) and `buildProviders`
  (`internal/metadata/chain.go`) returns the in-process provider for
  `kind='builtin'` rows instead of constructing a gRPC plugin.
- Guard rails keep the reserved row out of every plugin surface (user plugin
  settings, installations list, image resolvers, preload, auto-update, store
  delete, mutation handlers); `silo.builtin` is a reserved manifest id. Generic
  installation reads must **not** filter builtins — the chain enabled-check
  depends on them.
- On startup, `SyncBuiltinProviderChains` (`internal/metadata/builtin_sync.go`)
  first materializes legacy `content_level=''` chains per level, then appends
  builtin capabilities to all existing chains (enabled, since `nfo` is
  `default_enabled`) and moves builtin rows to the front. New libraries pick
  the capability up enabled and first through normal chain seeding, and the
  chain-less fallback includes it, except at a level where the library's
  local row is switched off. A save that omits the local row gets it back,
  enabled, at the next startup; turning local metadata off means saving the
  row disabled.
- Without `.nfo` files or sidecar art the provider returns nothing, so a
  library without local files behaves as before. Each refresh still looks for
  the candidate sidecar files.

## Precedence: locks, then local, then remote

Highest first: fields an admin locked while editing an item, then local
metadata (NFO text and sidecar artwork), then remote providers (TMDB, TVDB,
...), then filename parsing.

- **The local tier runs first.** Builtin installations are the local tier
  (`internal/metadata/local_tier.go`). `buildProviders` returns builtin
  providers ahead of plugin providers, which covers saved chains, the
  chain-less fallback and person refresh, whatever priority a row stores.
  `SetChain`, chain seeding and the startup sync store builtin rows first, so
  the API and the chain editor show the order that runs.
- **Fields the NFO sets win.** Phase 2 folds provider results fill-empty in
  chain order, so the NFO wins every scalar it supplies.
- **Lists the NFO supplies are the whole list.** Genres, studios, networks,
  countries, keywords, cast and crew (cast and crew count separately, like
  their locks) are not extended by remote results; remote providers fill only
  the lists the NFO left empty. Remote cast and crew enrich the NFO's credits
  that match by provider ID or by name and kind (IDs, photos, roles) and never
  add people (`internal/metadata/local_precedence.go`). Credits match only
  within their role, so a remote director credit fills an empty crew rather
  than folding into the same person's NFO actor credit. Lists from two remote
  providers still accumulate. In fill-empty writes (scheduled refresh,
  initial match of a stored item) an NFO list, credits included, fills an
  empty stored list but is not added to or swapped for a stored one; a manual
  refresh replaces it. The bulk
  enrichment pass has no record of which stored lists came from the NFO, so
  an enrichment-only provider can still add to them.
- **Locks win in every mode.** The persist merge applies the item's locked
  fields in initial match, scheduled and manual refresh, and Identify. Cast and
  Crew are separate locks; with only one set, the stored credits are read so
  the locked group survives the wholesale credit write, while the other group
  is replaced by the incoming one as it would be without a lock, or kept when
  the providers send none (`internal/metadata/people_locks.go`). An initial
  match of a skeleton item keeps a locked title and year.
- **One switch per library.** The chain editor shows the local entry as a
  pinned "Local metadata (NFO + artwork)" switch that sets every level's row at
  once and cannot be reordered. v2 marks the entry with
  `ProviderChainEntry.local`; the frozen v1 shape is unchanged. Text and
  artwork come from the same provider, so the switch turns both on or off.

## Reading NFO files

`internal/metadata/nfo/lookup.go` finds sidecars and `nfo.go`/`decode.go`/`ids.go`
parse them. The reader accepts what Kodi and Jellyfin accept, because a file
it rejects silently counts as "no NFO".

- **Names** match case-insensitively (`Movie.NFO`, `TVSHOW.nfo`, `Season.NFO`,
  `<basename>.NFO`) against a directory listing, exact spelling first. The
  provider caches listings by directory modification and change time (a copy
  tool can restore the first, not the second), so a flat folder of
  thousands of movies is listed once per change, not once per item and phase.
  A listing is cached only once its directory has gone a timestamp tick
  unchanged, since a file added in the same tick leaves the time as it was:
  a few seconds on a filesystem with coarse timestamps (a whole-millisecond
  modification time), 20 ms otherwise. A cached listing also carries an index
  of its stacked parts, built on first use, so a flat folder of many
  multi-part movies is swept once per change.
  A folder the server may traverse but not list falls back to the exact
  names; a miss there counts as unreadable, since a differently cased name
  can't be ruled out.
- **Order** follows Kodi: `<basename>.nfo` beside each media file, then the
  name a stacked movie's parts share (`Movie-cd1.mkv` and `Movie-cd2.mkv` →
  `Movie.nfo`, and `CD1.Feature.mkv` and `CD2.Feature.mkv` → `Feature.nfo`;
  the part tokens are the scanner's, only video files (never directories)
  count as parts, though they may differ in container or separators
  (`Film-part1-1080p.mkv`, `Film part2 1080p.mp4`), and a lone
  `Movie Part 2.mkv` never reads `Movie.nfo`), then `movie.nfo`/`tvshow.nfo`.
  Directory-level names are read only in the request's
  `PrimarySidecarSearchPaths`, which upstream restricts to single-content
  observed roots (`canUseObservedRootForDirectorySidecars`, the
  same guard sidecar artwork uses), so a stray `movie.nfo` in a flat
  multi-movie folder applies to none of its movies. A show's NFO is only ever
  `tvshow.nfo`, so episode files are not probed for it.
- **Limits:** files over 4 MiB, symlinked leaves, and non-regular files are
  skipped with a warning, as for sidecar artwork. A file replaced between the
  check and the read (an editor's atomic save) is read if it is still a
  regular file; a symlink or FIFO swapped in is reported unreadable (the open
  does not follow symlinks). A file that changes while it is read is read
  once more, then reported unreadable; a recently written file with a coarse
  timestamp is read twice and compared, since a same-size rewrite within the
  tick leaves its size and time unchanged.
- **Decoding:** one lenient decoder (`newNFODecoder`): valid UTF-8 is read
  as UTF-8 even under a stale legacy declaration, a declared legacy charset is
  decoded, other non-UTF-8 bytes are read as Windows-1252, UTF-16 with a BOM
  is converted, HTML entities (`&eacute;`) resolve, an HTML `<br>` closes
  itself, and neither a bare `&` nor a control character XML forbids rejects
  the file. Numeric elements (year, season, episode,
  actor order) are read leniently (`2.0`, a leading number followed by text);
  a value that is not a whole number drops only that field.
- **Link forms:** a URL-only file (just a provider link) yields identity hints
  and no fields; a hybrid file (XML followed by a link) adds the link's ids to
  the XML's. Links read: IMDb `tt…`, `themoviedb.org/movie|tv/<id>`, and TVDB
  series URLs that carry a numeric id. A TMDB movie link only names a movie
  and a TV or TVDB link only a show.
- **Unreadable is not absent.** A sidecar that exists but cannot be read (an
  I/O error, a permission problem), or a media file whose path no longer
  holds a file (the provider follows symlinks, as the scanner does) or a
  folder that is gone (an unmounted share often leaves an empty
  mountpoint; the provider only sees files the catalog lists as present, so
  it checks each one it relies on, and treats a series root or season folder
  that holds none of the show's or season's episode files the same way),
  makes the provider return
  `metadata.ErrLocalMetadataUnavailable`; a folder that has lost its media
  is not searched, so a stale `movie.nfo` or `tvshow.nfo` on the mountpoint
  is never applied; each lookup re-checks the media afterwards, so a share
  that drops mid-lookup is not read through. A missing NFO file is still
  "no NFO", and a readable
  candidate (another version's folder) still wins. A cataloged file deleted
  from a folder that still holds the item's other files is just gone (the
  share is mounted), so it is skipped rather than reported. Season and
  episode requests carry every cataloged file of the show for this check
  (`ShowFilePaths`), since a targeted refresh reads one season or episode
  and seasons can share a folder. When one
  path fails for permission and another because its share is gone, the
  lookup reports the share: the order of paths must not decide whether a
  match waits.
  - Passes that replace stored values stop: manual refresh, and the series
    season/episode target refresh in every mode (it replaces unlocked fields
    even when scheduled), which reads only the targeted season or episode.
  - An initial match waits: the outcome is `provider_transient`
    (`hasTransientMatchError` checks the sentinel) with a queue message that
    names the NFO, so the match queue retries instead of committing an
    identity the NFO may contradict. A permission problem does not clear by
    itself, so then the match goes ahead without the NFO, as it does when
    there is none (`localSidecarDenied`: not a provider error either).
  - Fill-empty passes (scheduled refresh, and a series' seasons and episodes
    at initial match) carry on. `GetSeasons`/`GetEpisodes` skip only the
    unreadable entry and return the rest with the error.
- The provider caches parses by file content, so the identity, search, and
  metadata phases of one pass parse a file once, and a rewrite is seen even
  when it keeps the file's size and modification time.

### Fields read

`nfo.go` maps every field Silo has a slot for:

- Titles and text: `<title>`, `<originaltitle>`, `<sorttitle>`, `<tagline>`,
  `<plot>` (then `<outline>`), `<mpaa>` (then `<certification>`, whose
  country-tagged list such as `US:PG-13 / GB:12A` gives its first rating), and
  `<originallanguage>` when it is a language code. `<language>` is not read:
  Jellyfin and Emby write the library's metadata language there.
- Dates and numbers: `<year>`, `<premiered>`/`<releasedate>`/`<aired>`, and
  `<runtime>` as minutes, also in the "117 min" and "1h 57m" spellings.
- Lists: `<genre>`, `<studio>`, `<country>`, `<director>`, `<credits>` and
  `<writer>` (both writers, one credit per name), split on Kodi's ` / `
  separator. A show's `<studio>` also fills its networks; Kodi has no network
  element. `<tag>` is not split.
- Shows: `<status>` through `NormalizeShowStatus`.
- Trailers: `<trailer>` as a YouTube or Vimeo video, from the YouTube add-on
  link Kodi's scrapers write (`plugin://plugin.video.youtube/...`) or a web
  link. A link to any other site is dropped: videos are stored as a site and a
  key, and clients play only the sites they know.
- Ratings (`ratings.go`): each named `<rating>` fills its source once, on its
  headline scale (IMDb and TMDB out of 10, Rotten Tomatoes out of 100), and the
  sources beyond IMDb and TMDB also fill `RatingSources` with their `<votes>`.
  The provider declares those sources (Rotten Tomatoes, Metacritic, Trakt,
  TVDB, Letterboxd); a name that maps to none is dropped. IMDb and TMDB stay
  out of `RatingSources`: their rows hold the vote counts discovery ranks by,
  which an NFO's frozen or missing `<votes>` must not replace. The legacy bare
  `<rating>` (with its `<votes>`) fills the IMDb slot only in an NFO without a
  `<ratings>` block: a writer that emits both, such as Radarr, copies its
  default source there, which is TMDB until IMDb has a score. Jellyfin's
  `<criticrating>` is the Rotten Tomatoes critics score.
- People: `<actor>` with `<order>` (or Jellyfin's `<sortorder>`), `<type>`
  (GuestStar, Director, ...), and the `<tmdbid>`/`<imdbid>`/`<tvdbid>`
  tinyMediaManager writes; its `<thumb>` is described below. Merging credits
  matches a provider ID, else a name, within one role group (the cast, actors
  and guest stars together, or one crew kind), so a person credited as both
  actor and director keeps both credits.
- Episodes: `<uniqueid>`s become the episode's provider ids, unless the file
  describes several episodes, serves several (one NFO beside a
  multi-episode video), or declares numbers other than the filename's. The
  air date falls back from `<aired>` to `<premiered>` and `<releasedate>`. Episodes have
  no Rotten Tomatoes columns, so those ratings are read but not stored.
- Seasons: a tvshow `<namedseason number="N">` titles season N when its
  `season.nfo` gives no title; `season.nfo` reads `<title>` (or
  `<seasonname>`) and `<premiered>`/`<aired>` as the season's air date.

## Identity: hint-first, never candidate-first

NFO ids are **trusted identity hints** seeded before remote search
(`IdentityHintProvider`, applied in `internal/metadata/service.go`),
unlocking full remote enrichment even when search-by-title fails. The forms
read, strongest first: typed `<uniqueid>` (type spellings such as `TMDB`,
`themoviedb`, `tmdb_id`, `TheTVDB` are normalized; `default="true"` wins
within a type), an untyped or `unknown` `<uniqueid>` holding an IMDb id,
`<imdbid>`/`<imdb_id>`/`<tmdbid>`/`<tvdbid>`, the legacy `<id>` (its
TMDB/TVDB/IMDB attributes, or a `tt…` value; a bare number names no provider),
a tvshow `<episodeguide>` (Kodi 20 JSON or the older TVDB/TMDB API URLs), and
a hybrid file's trailing link. Values are checked with the pipeline's own
rules (`internal/providerid`), so a malformed value (`N/A`, letters in a TMDB
id, a `0` or `tt0000000` placeholder) claims nothing and a weaker valid form
still supplies the id. Conflict policy
by mode:

- initial match: NFO ids replace folder- and filename-tag ids **as a set**
  (`dropIDsNotRestatedByHints`): a tag id the NFO does not restate is dropped
  rather than left as a trusted hint that would make the NFO's own title fail
  selection. Once an NFO id that a remote provider returned survives the
  lookups (or, in a chain with no remote search provider, the NFO's ids
  alone), the replaced tag ids are rejected for the run, so
  they are not persisted from the scanner's tag-anchored skeleton, and the
  skeleton moves to the NFO's anchor (as a correcting Identify does) so the
  tag's own title cannot later merge into it. A dead NFO identity leaves the
  tag ids alone: when a provider rejects every id the NFO named, the search
  runs again with the tag ids (less any the providers rejected), and an NFO
  id recorded stale on an earlier attempt is not used as a hint, and the tag
  id it replaced comes back even if the NFO names another provider. When a
  transient failure keeps every remote provider from returning one of the
  NFO's ids, the match waits rather than re-anchor on an id nothing checked;
  a failure after one did (an enrichment quota) does not hold it. When a
  provider confirms an NFO id in search and then rejects it (a 404 on its
  details), the match also waits; the retry, with that id recorded stale,
  searches with the tag ids. Stored
  durable IDs beat NFO hints;
- scheduled refresh: stored durable IDs beat NFO hints (no background identity
  flips);
- manual refresh: NFO hints beat stored IDs — the recovery path for a corrected
  NFO. The item's `content_id` re-anchors too (not just its stored provider
  IDs): a provider-anchored item whose fixed `<uniqueid>` now derives a
  different anchor is renamed to it, or merged onto the existing item already
  holding that id;
- manual Identify: the user's choice wins; the NFO provider is skipped for the
  whole operation. The choice is sticky: every Identify (including the one a
  split runs for its target) sets `media_items.identity_chosen_at` in the
  transaction that writes the identity it chose, and also in a merge that
  commits before that write onto the item already holding the chosen IDs (the
  row at the choice's anchor, or the owner of its provider IDs). The mark
  always commits with the chosen IDs: a re-anchor rename keeps the old IDs
  until the identity write, so it leaves the mark to that write. Renames and
  merges carry the mark along, and a later refresh whose NFO names a different
  identity keeps the chosen one. Running Identify again is how an admin
  changes it.

Whenever a run keeps an identity the NFO's IDs contradict, it logs the
conflict and leaves the NFO provider out of every phase of that run (series,
season and episode), since its text, lists and art describe the other title
(`internal/metadata/identify_sticky.go`). Runs that keep the stored identity
(scheduled refresh, initial match of a stored item, and a refresh of one
season or episode, judged by the series' own files) treat a declared ID that
differs from the stored one for the same provider as a conflict; an ID for a
provider the item lacks only adds to it. After Identify, every run, manual
refresh included, treats any declared IDs that don't confirm the chosen
identity as a conflict. A stored ID already recorded as dead (a 404 in
`stale_media_ids`) doesn't count as identity, so an NFO naming its replacement
applies. A title-only NFO declares no identity and always applies.

The NFO search candidate exists only for the title-only case and is defanged:
ID-less candidates lose provider-priority tie-breaks to ID-bearing group
members, `nfo` never counts as corroboration, and Phase-2 NFO results cannot
inject provider-id keys. A title-only NFO yields a `matched` item under a
path-deterministic `local-` content id; adding a `<uniqueid>` later promotes it
to that provider anchor on the next refresh (this promotion from an unanchored
id runs on scheduled refresh too — only an *already-anchored* identity is held
stable in the background). The scheduled refresh stays fill-empty; NFO field
edits reach a matched item through [change detection](#change-detection) or a
manual refresh.

## Sidecar artwork: copy into the S3 image cache

Sidecar images are discovered by the provider's `GetImages`
(`internal/metadata/nfo/images.go`) and its season and episode lookups
(`internal/metadata/nfo/series_depth.go`). Clients — including jellycompat —
always receive the normal presigned `poster_url`/`backdrop_url`/`logo_url`
(and, on `/api/v2` movie and series detail, `banner_url`, `thumb_url`,
`clearart_url`, `disc_url` and `backdrop_urls`); library files are never served
directly. The admin image picker (below) is the only API path that reads
sidecar files.

Discovery lists each folder once per request and matches names ignoring case,
so `Poster.JPG` and `Folder.jpg` count on case-sensitive filesystems; an entry
spelled exactly as listed below wins over other spellings. A folder that can
be searched but not listed (execute-only permission) still serves names looked
up exactly. Listings of folders with 256 or more entries (a flat folder of many
movies) are shared across requests for up to a minute while the folder's mtime
is unchanged, and only once that mtime is a few seconds old (coarse filesystem
timestamps), so such a folder is not listed again for every movie in it. Names
in
precedence order, each tried with `.jpg`, `.jpeg`, `.png`, `.webp`, `.tbn`,
`.gif` (the formats libvips decodes; `.tbn` is a JPEG):

| Slot | Names |
| --- | --- |
| Movie poster | `<basename>-poster`, then `poster`, `folder`, `cover`, `default`, `movie` |
| Series poster | `poster`, `folder`, `cover`, `default`, `show` |
| Backdrops | movie `<basename>-fanart`, then `fanart`, `backdrop`, `background`, `art`, then the numbered names and `extrafanart/` (below) |
| Logo | movie `<basename>-logo`, `<basename>-clearlogo`, then `logo`, `clearlogo` |
| Banner | movie `<basename>-banner`, then `banner` |
| Thumb (landscape) | movie `<basename>-landscape`, `<basename>-thumb`, then `landscape`, `thumb` |
| Clear art | movie `<basename>-clearart`, then `clearart` (`art` is a backdrop) |
| Disc art (movies only) | `<basename>-disc`, `<basename>-cdart`, `<basename>-discart`, then `disc`, `cdart`, `discart` |
| Season poster | `poster`, `folder`, `cover` in the season folder, then `seasonNN-poster` (`season-specials-poster` for season 0) in the series root |
| Episode still | `<basename>-thumb`, then `<basename>` |

- Basename names attach to one of the item's own media files and beat generic
  names, as in Kodi. A series has no media file of its own (its request lists
  every episode file), so a series uses generic names only and
  `S01E01-poster.jpg` never becomes series art. Every other slot takes the
  first match.
- An item keeps up to 20 backdrops (`metadata.MaxItemBackdrops`), the first
  match being the primary (`media_items.backdrop_*`) and the rest stored in
  order in `media_item_backdrops`. After the named backdrops come the numbered
  families, in Jellyfin's order: `fanart-N` (or Kodi's `fanartN`),
  `background-N`, `art-N`, `backdropN`, each from 1 to 20 and stopping after
  three missing numbers in a row (Jellyfin stops after three misses in total,
  so Silo finds a superset). Then every image in an `extrafanart` folder,
  sorted by name. Jellyfin's `backdrops` folder holds theme videos and is not
  read.
- Generic names attach only in folders that hold a single content group; a
  shared `folder.jpg` in a flat multi-movie folder applies to none. A season
  folder that is the series root itself (a flat series) is not searched for
  season names, so the series' own `poster.jpg` is not reused as every
  season's poster.
- Files must be regular (symlinked leaves are rejected), non-empty, and at most
  25 MiB (`metadata.MaxArtworkSourceBytes`, which also caps provider
  downloads in `internal/imagecache`). Skips an admin can fix (symlink, oversize, unreadable) log at
  warn level; the rest at debug.

Caching and refresh:

- Sources are recorded as `file://<absolute-logical-path>?v=<size>-<mtime>`
  (mtime in whole seconds, so mounts reporting different sub-second precision
  agree) in `*_source_path` columns. The fingerprint exists only to change the source
  path when a file is replaced under the same name: the changed source
  re-queues the image-cache job (`EnqueueBatch` requeues on a different
  `source_path`), and the processor strips the fingerprint before confining
  and reading the file. Other consumers treat `file://` as a scheme prefix
  only. The first refresh after this fingerprint was introduced re-queues
  every local slot once.
- The metadata image-cache processor caches the file under
  `local/{contentType}/{contentID}/{hash8}/{imageType}/...` (`hash8` = content
  hash; the discriminator sits before the imageType segment because variant
  clamping reads the parent directory). A replaced image rotates the key; the
  stale hashed prefix is deleted after a successful re-cache, and item
  deletion trims local paths to the content prefix.
- The process that discovered the sidecar copies it
  (`internal/metadata/local_artwork_copy.go`). Only `api` and `integrated`
  processes run refreshes and the image cache, and a refresh can only discover
  a file it can read, so that process sees the library mount. The refresh hands
  the local slots whose files it sees (item, season, episode; the copier takes any target type, and
  a person photo carries the owning item's content ID as `series_id` for root
  confinement) to `ImageCacheProcessor.CopyLocalArtwork`, which queues the jobs
  behind a 10-minute discovery hold that other drains respect and copies them
  on two background workers per process, claiming each job by slot. That claim
  ignores the hold and a missing-storage deferral, but not the backoff of an
  attempt that failed. The copies
  share one per-process budget with the queue drain and manual refreshes,
  sized from `metadata.image_workers` and its memory cap. Freed slots go to
  waiting callers in arrival order, a manual refresh ahead of background work;
  a claimed job that cannot start in time to finish inside its lease goes back
  to the queue.
  Refreshes never wait for a copy, and a manual refresh claims its own held
  discoveries without making them due for other servers, handing back any it
  cannot start still held. A slot that does not fit the in-memory backlog
  waits out the hold, after which any drain copies it.
- The queue keeps local jobs for retries, and any server may claim one. The
  processor confines each read to the owning library's `media_folders` roots:
  a lexical check on the logical path, then a symlink-resolving re-check (both
  path and roots are resolved, so a legitimately symlinked root stays valid
  while an intermediate directory symlink escaping a root is rejected). Each
  library root is opened as an `os.Root` before the path is resolved, and the
  file is opened through the handle that is the same directory as the resolved
  root, so a root or folder swapped for a symlink after that point cannot lead
  the open out of the library. When
  the re-check or the read fails, the processor probes, through
  `internal/rootcheck`, the nearest folder that exists at or above the file's
  folder, searching no higher than the library root: unless it lists entries
  or exists but denies listing (or is reached through a symlink out of the
  library, so the path escapes it), the storage is not mounted on this server (an
  empty or missing mount point, also for a share nested inside a present
  root, or a folder that fails to read or does not answer in time). A folder
  removed under mounted storage is therefore a stable failure, not a deferral. A
  deferred job goes back to the queue without counting an attempt, for a
  server that has the mount: after 10 minutes, or an hour when it was last
  deferred for the same reason, so storage no server mounts any more stays
  cheap. A local source that a refresh on a server without the mount keeps
  (see retention below) is queued the usual way, not handed to that server's
  copier, so its hold or deferral stays as it was; and a copy that claims a
  deferred job early leaves it its remaining wait (at least 10 minutes). A
  manual refresh makes such a job due again and starts its deferral
  over, and reports its artwork as still pending while a job of its target
  waits this way. Otherwise missing/unreadable/out-of-root and
  structurally-unusable (non-regular, over-cap) paths are stable failures with
  a long retry deferral; recovery is refresh-driven — the provider-artwork
  backfill sweep deliberately skips `file://` sources.
- Until the copy lands, a slot keeps serving the copy it already has. With
  nothing cached, it serves the best remote image the refresh found: for items
  the best remote candidate by the `applyBestImages` rules, for seasons and
  episodes the first remote poster or still that the local one displaced while
  provider results merged, or, when the refresh kept a local source it could
  not see, the remote one it chose. The slot's source stays the local file, so the
  copy replaces the stand-in through the source-matched update, and a refresh
  that offers no artwork at all leaves the slot, local source included, as it
  is. Locked artwork gets no stand-in, and neither does an image URL an NFO
  names: its slot stays empty until the image cache has downloaded it.
- With `metadata.cache_images` off, clients load artwork straight from the
  providers, so no local artwork is used (`localArtworkUsable`): refreshes,
  targeted season and episode refreshes, scan re-applies, and person photos
  drop local candidates, items keep no extra backdrops, and the image cache
  claims, copies, and downloads nothing. NFO text still applies. A refresh
  also stops keeping stored copies: `preserveCachedArtwork` lets a provider
  path replace one unless the artwork is locked, and an unlocked item's stored
  poster, backdrop, and logo count as empty for `applyBestImages`, so even a
  fill-empty refresh takes an unrated provider image. Artwork stored before the
  setting went off is released by `StoredArtworkReleaser` (see
  [settings-api.md](../settings-api.md)).

Precedence: local artwork beats remote providers wherever the NFO provider
sits in the chain.

- Items: `applyBestImages` always applies a local candidate, so rating-0 local
  art fills already-matched items.
- Seasons and episodes: `accumulateSeasonResults`/`accumulateEpisodeResults`
  merge first-provider-wins, except that a local poster or still replaces a
  remote one from an earlier provider (`internal/metadata/local_artwork_precedence.go`).
  The remote poster a local one displaced stays on the merged result, and a
  write that touches only a season's localization row (a refresh in a
  language other than the item's canonical one) stores that provider poster,
  as item localization rows skip local candidates.
- A refresh that does not rediscover a slot's local art (a `file://` source,
  served from its cached `local/` copy or, while that copy is pending, from
  what the slot served before or a remote stand-in) keeps it, so a server
  without the mount cannot hand the slot and its queued job to a provider,
  unless the sidecar's own folder answers
  and lists entries: then the file is either missing or still on disk but no
  longer offered (renamed out of the naming rules, replaced by a better match,
  or local metadata disabled), and the slot takes the provider's art. A folder
  that is missing, empty, unreadable or slow to answer is what an unmounted
  share or a vanished nested mount looks like, so the local copy stays. When
  an item's media moves, the old art therefore stays only while its former
  folder is gone; a former folder that still lists entries proves the library
  is mounted, so the slot takes the new folder's local art or, without any,
  the provider's. The folder is probed through `internal/rootcheck` with its
  timeout, once per folder, before the sidecar itself is looked up. This
  applies only at a level (item, season, episode) where the local provider
  answered a request for artwork in this refresh (`withLocalArtworkSearched`):
  with local metadata off for that level, during an Identify, in a run that
  leaves local metadata out because the NFO names another title (see
  Identity above), or when the local provider's call failed, cached local art
  is stored artwork like any other and no library folder is probed. A slot
  such a refresh, or one with locked artwork, leaves on its remote stand-in
  keeps its local source, as a slot left on its cached copy does.
- Banner, thumb, clear art, disc art and the backdrops after the primary come
  only from sidecars today, so the rule above has no provider art to fall back
  to. When a refresh searched the item level and did not return a slot's
  sidecar, the slot is cleared once the sidecar's folder answers (the file is
  missing or no longer qualifies) and kept while it does not. A refresh that
  did not search the item level leaves the slots alone. Their cache jobs and
  storage follow the poster's; an extra backdrop's job targets its
  `media_item_backdrops` row (`item_backdrop`), and its local key carries a
  hash of the sidecar's path beside the content hash. Two sidecars with the
  same bytes get different keys, so re-caching one backdrop never deletes
  another's prefix, and a backdrop that moves to another row keeps its key. A
  stored backdrop the refresh did not find again stays unless its sidecar is
  gone; a removed `extrafanart` folder counts as gone once the folder holding
  it answers.
- Localization: local art is language-neutral. Localization rows skip local
  candidates, and the read-side overlay (`internal/catalog/localize.go`) keeps
  base art served from a `local/` key (`artworkkey.IsLocal`), so a viewer in
  another language sees the same local art. A slot whose local copy is not
  cached yet still serves its earlier art or remote stand-in, which a
  localized image may replace.

### Artwork and photos an NFO names

An NFO's `<thumb aspect="...">` (no aspect is a poster, tried after the
thumbs that name an aspect, and never a TVDB banner path), `<fanart><thumb>`, a
tvshow's `<thumb season="N">`, and an episode's `<thumb>` fill the poster,
logo, backdrop, season poster, and episode still slots (`nfo/art.go`). A
reference is a path relative to the NFO or an http(s) URL:

- A local path must stay inside the folders holding the item's media files or
  directory-level sidecars, also with symlinks resolved, and pass the sidecar
  checks. It becomes a fingerprinted `file://` source, like a sidecar's.
- A URL loses credentials and literal non-public addresses up front and
  becomes an `nfo+http(s)://` source (`metadata.LocalMetadataURLSource`). The
  NFO's author is not a trusted provider, so such a URL is handled like a
  sidecar file: `isLocalImageSourcePath` counts it, it waits in
  `*_source_path` until the image cache downloads it through its
  public-address guard, it is never served to clients or fetched by
  jellycompat's image proxy, and, like all local artwork, it is not used
  while `metadata.cache_images` is off. Like a sidecar, it is re-copied by a
  refresh or the ladder backfill, not by the provider-artwork sweeps. Its
  copy is attributed to the `nfo` provider and keyed like a provider's
  (`nfo/{contentType}/{contentID}/{imageType}/...`); only `file://` copies
  use the content-hashed `local/` keys. `artworkkey.IsLocal` counts both as
  library artwork, so a viewer's localized provider art does not replace
  either. A refresh keeps the copy until it reads the NFO and the NFO no
  longer names the URL: a fill-empty refresh that could not read the NFO
  (`withLocalMetadataUnreadable`) keeps it, as it keeps a sidecar copy on a
  share that does not answer.

Banner, clear art, disc, key art, and landscape references are parsed with no
slot to fill: `nfoArtImageTypes` is where a new slot connects.

Precedence: a sidecar file wins its slot, so the provider offers NFO-named art
only for slots discovery left empty; being local, NFO-named art then outranks
every remote provider in `applyBestImages`, and an NFO-named season poster
or episode still replaces a remote one in the season and episode merges.

An actor's `<thumb>` resolves the same way into the credit's
`LocalPhotoSource`. People are shared across titles, so after the credits are
stored `applyLocalPersonPhotos` gives a person the photo only when they have
none (nothing, or `-`), or when their photo came from the same NFO (the same
source, or a local file in the same folder). A provider's photo, even one
still waiting for its copy, stays. It queues a person image-cache job whose `series_id` is the
title, so the processor confines a local read to that title's library roots;
a title's interactive artwork refresh leaves those person jobs out of its
scope. A person refresh keeps a cached NFO photo over a provider's. Like other
local artwork, NFO photos are used only while `metadata.cache_images` is on;
photos already copied stay (the stored-artwork release leaves people alone).

### Admin image picker

`GET /api/v2/admin/items/{id}/images` also offers a movie's or series' local
sidecar artwork. The built-in sidecar discovery runs with the item's media files
and sidecar directories, so it finds the same files a refresh would, and it runs
whether or not the library's metadata chain uses the NFO provider: choosing an
image is an explicit admin action. Local choices come only from that discovery;
a `file://` URL returned by any provider in the chain is dropped, so a provider
cannot get another library file offered as this item's artwork. Only sidecar
files (including files an NFO names by relative path) for the slots an admin can
apply (poster, backdrop, logo) are offered: an image URL an NFO names has no file
to read here, and banner, thumb, clear art and disc art cannot be applied. Each
local choice has the provider ID `local`, keeps its fingerprinted `file://`
source as `original_url`, and shows a 300-pixel-wide WebP preview as an inline
`data:` URI (the web client authenticates with a bearer token, so an `<img>`
cannot fetch an authenticated preview route). Local choices lead the list, ahead
of every provider's, in discovery order, so each slot's first local choice is
the sidecar file a refresh uses when it takes local artwork. The preview is read under the same
root confinement, symlink and size checks as the processor; a file that fails
them is left out of the list, and the list's `provider_errors` reports it under
`local`. Only the movie and series list does this: any other list treats a
provider's `file://` URL as an ordinary provider choice and never reads it.

`POST .../images/apply` with a `file://` `original_url` caches the file only
when the item's sidecar discovery offers that exact source for that image type,
so a file replaced since the list (a new fingerprint) must be picked again. It
reads the file under the same checks, stores it under the same `local/...` key
the processor would use, and publishes the fingerprinted source with the images
lock like any other choice. Seasons, episodes and frozen v1 keep provider-only
choices. The server answering the admin API reads the file, so it needs the
library mounted, like a server that copies local artwork; on a server without
the mount discovery finds nothing and local choices are not offered.

The reads open each library root as an `os.Root`, which needs read (list)
permission on the root directory, not only search permission. The scanner lists
the roots, so a server that scans a library already has it.

## Series depth and mixed libraries

`SeasonsRequest`/`EpisodesRequest` carry local path context; `season.nfo`
(and the show's `tvshow.nfo`) supply season name/plot/air date, `<basename>.nfo`
and the episode still (see above) supply episode metadata/thumbs
(`internal/metadata/nfo/series_depth.go`). **Naming
supplies structure; NFOs supply metadata**: NFO season/episode numbers are
advisory — directory/filename-derived numbers win with a warning. Episode NFOs
work without a `season.nfo`; `SynthesizeFallbackEpisodes` still covers
NFO-less episodes.

In `mixed` libraries, movie-vs-series is decided per file by naming before any
provider runs (`internal/naming/filename.go`); the NFO provider is seeded into
all levels and behaves as in dedicated libraries. The provider's root-element
type guard means a `tvshow.nfo` next to a movie-classified file is ignored,
never applied; the per-root Type override is the correction path. This is the
contract for sports/mixed libraries (events as movies, weekly shows as series).

## Change detection

A matched item picks up an edited NFO or sidecar image without a manual
refresh (`internal/metadata/local_sidecar.go`, `local_reapply.go`).

- **Fingerprints.** `local_sidecar_fingerprints` stores, per library and per
  item, season and episode, a SHA-256 of the path, size and modification time
  of the sidecar files the local provider applied. The provider reports those
  files itself (`LocalSidecarProvider`, `internal/metadata/nfo/sidecars.go`)
  with the same lookups as `GetMetadata`/`GetImages`/`GetSeasons`/`GetEpisodes`,
  so lookup rules live in one place. A season's files include the show's
  `tvshow.nfo` where it names the season or its poster, and an item's include
  every image slot and backdrop `GetImages` fills. Item rows also
  store a digest of every sidecar-type file in the item's folders (its files'
  directories and observed root) and the provider IDs the NFO declared. Rows
  follow a content ID rename and go with their library.
- **Equality, not timestamps.** A change is any difference in the applied
  set, a size, or a modification time: an edit, a new higher-precedence file,
  or a removed one. Silo never compares a file's time with when it last saved
  the item.
- **Scans** (library, subtree and file). The video walk keeps the names of the
  sidecar files in every directory it reads (`scanner.SidecarListing`) at no
  extra filesystem cost. After matching, `libraryingest` calls
  `CheckLocalSidecars` for the scanned scope. In libraries that take local
  metadata, each listed directory's sidecars are stat'ed once; an item whose
  folder digest is unchanged costs nothing more. Otherwise the provider
  lookups run, and only a changed fingerprint re-applies. A scan narrower than
  the item (one season's folder) checks only the seasons and episodes it read
  and keeps the stored folder digest, so the next full scan still sees changes
  in the folders it skipped. A changed item gets a `catalog.item.changed`
  event (at most 100 per scan).
- **Events.** For file changes from the library monitor and autoscan,
  `scantrigger` resolves an `.nfo` or sidecar image in a video library to a
  subtree scan of its folder (`Request.SidecarFolder`; the scan API still
  rejects non-media paths). One directly under the library root is not
  routed, so one edit in a flat library never becomes a full library scan;
  the next library scan picks it up (see Known limitations). The
  Jellyfin-compatible autoscan endpoint already scans a rejected file's
  parent folder. The monitor's quiet window and the scan queue's coalescing
  debounce bursts.
- **Scheduled refresh.** `refresh_metadata` checks each item target it visits
  with `CheckLocalSidecarTarget`, from the item's applied files alone, before
  its own refresh, which can move a local item to a new content ID. When the
  check re-anchored the item (a changed identity), the scheduled refresh runs
  under the item's new content ID. A re-anchored item's record is stored, and
  its change published, under the content ID its media file names after the
  refresh.
- **Re-apply.** Local files only, no remote provider call. The item's NFO
  values merge with `ReplaceUnlocked` through `mergeAndPersist`
  (`localReapply`), so field locks win and lists the NFO supplies replace the
  stored ones; credits keep the provider IDs and photos the stored cast
  already has. Local artwork applies as on any refresh, so an image replaced
  under the same name gets a new `?v=` source and is cached again; identity,
  videos and refresh bookkeeping are left alone. Seasons and episodes merge their local
  results with `ReplaceUnlocked` through `persistSeasonAndEpisodeRows`.
- **Chain order.** A level's local provider always runs ahead of its remote
  ones (see Precedence), so wherever a level has one, its NFO text, artwork
  and identity are tracked and re-applied. A level without one records no
  fingerprint; adding the local provider there starts the next check that
  looks the item up from first sight.
- **Symbolic links.** The folder digest records whether each sidecar name is a
  link, and the size and time of what it names: an edit to a linked NFO is
  seen, and so is replacing a linked image (which discovery skips) with a copy.
- **Identity.** The NFO's IDs are compared with the item's identity as a
  refresh compares them (see Identity): IDs recorded as dead count on neither
  side. When the NFO now declares an ID it did not declare last time
  (removing one never counts), and that ID contradicts the item's or gives a
  locally matched item its first provider ID, the change runs a manual
  refresh, which re-anchors the item as described under Identity. An admin's
  Identify choice (`media_items.identity_chosen_at`) outranks any declared IDs
  that don't confirm it: the item keeps its identity, nothing local is
  applied, and the conflict is logged. The same holds when the NFO's IDs did
  not change but contradict the item's. A series whose NFO names another
  title by these rules applies none of its season or episode files either.
- **First sight records.** An item with no stored row, or with one made while
  its level had no local provider, is recorded without re-applying, so
  enabling local metadata or upgrading never rewrites a library; edits made
  after that are detected. The first record covers all of the item's seasons
  and episodes, even when a scan of one season folder makes it. A season or
  episode that appears in an item already recorded is new, so its sidecars
  apply. An item whose media file cannot be read (an offline mount) is
  skipped, so its sidecars never read as removed, and an item whose files
  cannot be loaded from the database records nothing. Nor does one whose NFO
  lookup fails (no NFO applies and one cannot be read, or its share is not
  there), as `GetMetadata` would fail; or a series seen for the first
  time whose seasons and episodes could not be checked: it stays in
  first-sight mode until they are. A re-applied series stores its folder
  digest only after its seasons and episodes were checked, so a scan stopped
  in between leaves them to the next one. A first-sight record never replaces
  one another check stored meanwhile. A fingerprint advances only after its
  re-apply succeeds, including its credits and queueing its artwork, and only
  when every lookup it needs (provider chain, observed roots, library
  language, Identify choice) succeeded, so a failure is retried by the next
  check. Season and episode rows are the exception listed under Known
  limitations: a failed row write still advances its fingerprint.
- Everything runs inside existing queued work (`scan_runs`, the
  `refresh_metadata` task) on API/integrated nodes. A re-apply and its stored
  fingerprints happen under a Postgres advisory lock per item and library,
  after the record and files are read again, so two checks on any nodes apply
  one change once and never store a fingerprint newer than the item shows.
  The rest of a scan's records are written in batches outside the lock: a
  first record only where none exists, any other only while the stored
  record still holds the fingerprint the scan read, so a batch never
  overwrites what another check stored meanwhile. A
  check that finds the lock held waits up to 30 seconds, so an edit saved
  during another check's re-apply is applied right after it; past that it
  stores nothing and the next check looks again. PostgreSQL enforces the wait
  (`pglock.Acquire`), so a check that gives up leaves no session queued on the
  lock. A process holds or waits for at most a quarter of its pool's
  connections' worth of these locks, and never more than four, since each pins
  a pooled connection while its re-apply runs. The wait covers getting that
  connection too. With a pool of two connections or fewer the lock takes a
  connection of its own, one per process, so the re-apply keeps the pool's.

## Known limitations

- The admin image picker offers local art for movies and series only, not
  for seasons or episodes.
- Seasons have no banner, thumb or extra backdrops (`seasonNN-fanart`,
  `seasonNN-banner`, `seasonNN-landscape` and their season-folder forms are not
  read).
- NFO `<thumb aspect="banner|landscape|clearart|discart">` and `<fanart>` URLs
  are not read. A provider that offers banners, thumbs, clear art or disc art
  returns `RemoteImage` entries of type `ImageBanner`, `ImageThumb`,
  `ImageClearArt` or `ImageDisc` (plugin image kinds `banner`, `thumb`,
  `clearart`, `disc`), and `applyBestImages` fills the slot when no sidecar
  does.
- A value removed from an NFO keeps its stored value: Silo records no
  per-field provenance, so re-applying cannot tell an NFO-supplied value from
  a remote one. Removing a sidecar image likewise keeps the cached copy until
  a manual refresh.
- A sidecar directly under a library root (a flat movie folder) is not routed
  by file events; the next library scan picks it up. In a flat folder every
  item shares the folder digest, so one change there runs the provider lookups
  for each item in it.
- Changed season and episode rows are written by the shared
  `persistSeasonAndEpisodeRows`, which logs a failed row, or a failure to
  queue their artwork for caching, instead of returning the error. Their
  fingerprints still advance, so such a row keeps its old values or artwork
  until its sidecar changes again or the series is refreshed.
- An item whose files span several roots is checked only while every root is
  reachable, since a sidecar on an offline root reads as removed.
- Two checks recording an item for the first time at once may take an edit
  saved between their reads as the baseline, when the later read is stored
  first.
- Images in a subfolder of an item's folder (`extrafanart/`, or one an NFO
  names) are part of its fingerprint and apply with every re-apply, but scans
  and file events watch only the item's own folders, so a change there alone
  waits for the scheduled refresh's check or the next sidecar change beside
  the item.
- An actor photo an NFO names (an `.actors` image) is not fingerprinted:
  replacing it under the same name takes effect at the item's next re-apply
  or refresh.
- Multi-part movies: in a folder holding multiple content groups, only
  `<basename>.nfo` and the stack base name are read; a differently named NFO
  there is not found.
- No NFO writing, no music/audiobook/ebook NFO (those ecosystems use
  OPF/metadata.json — future builtin providers via the same registry).
- Feature detection: the `nfo` capability's presence in
  `GET /api/v1/libraries/provider-defaults` (see
  `docs/architecture/v1-scope.md`); in v2 an entry with `local: true`.
- `<set>` (movie collections) is not mapped: `MetadataResult` has no
  collection field, so `nfoCommon` (`internal/metadata/nfo/nfo.go`) does not
  decode it.
- `<userrating>` is parsed into `UserRating` but never emitted; Silo has no
  per-user rating slot in `MetadataResult`. `TestParseNFOData_TableCases`
  covers a document with only `<userrating>` producing no ratings.
- Multi-episode NFOs (several `<episodedetails>` roots in one file) are not
  supported. The parser keeps the first block and sets `MultiEpisode`, and
  `internal/metadata/nfo/series_depth.go` logs a warning with the file path
  (`TestParseEpisodeNFO_MultiEpisodeDocumentTakesFirst`).
