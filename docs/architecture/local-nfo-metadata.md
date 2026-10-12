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
movie/series/season/episode and `default_enabled=false`
(`migrations/sql/20260712100125_builtin_host_providers.sql`,
`...124741_nfo_capability_season_episode_levels.sql`).

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
  builtin capabilities to all existing chains, disabled. New libraries pick the
  capability up through normal chain seeding. The chain-less fallback respects
  `default_enabled=false`.
- **Everything is inert until an admin enables "NFO Files" on a library.**

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
  whole operation.

The NFO search candidate exists only for the title-only case and is defanged:
ID-less candidates lose provider-priority tie-breaks to ID-bearing group
members, `nfo` never counts as corroboration, and Phase-2 NFO results cannot
inject provider-id keys. A title-only NFO yields a `matched` item under a
path-deterministic `local-` content id; adding a `<uniqueid>` later promotes it
to that provider anchor on the next refresh (this promotion from an unanchored
id runs on scheduled refresh too — only an *already-anchored* identity is held
stable in the background). NFO field edits propagate on **manual refresh only**
(scheduled refresh is fill-empty by design).

## Sidecar artwork: copy into the S3 image cache

Sidecar images are discovered by the provider's `GetImages`
(`internal/metadata/nfo/images.go`) and its season and episode lookups
(`internal/metadata/nfo/series_depth.go`). Clients — including jellycompat —
always receive the normal presigned `poster_url`/`backdrop_url`/`logo_url`;
library files are never served directly, and API nodes never need filesystem
access to libraries.

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
| Backdrop | movie `<basename>-fanart`, then `fanart`, `backdrop`, `background`, `art` |
| Logo | movie `<basename>-logo`, `<basename>-clearlogo`, then `logo`, `clearlogo` |
| Season poster | `poster`, `folder`, `cover` in the season folder, then `seasonNN-poster` (`season-specials-poster` for season 0) in the series root |
| Episode still | `<basename>-thumb`, then `<basename>` |

- Basename names attach to one of the item's own media files and beat generic
  names, as in Kodi. A series has no media file of its own (its request lists
  every episode file), so a series uses generic names only and
  `S01E01-poster.jpg` never becomes series art. Silo stores one backdrop, so
  the first match wins.
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
- The processor confines each read to the owning library's `media_folders`
  roots: a lexical check on the logical path, then a symlink-resolving re-check
  (both path and roots are resolved, so a legitimately symlinked root stays
  valid while an intermediate directory symlink escaping a root is rejected).
  Missing/unreadable/out-of-root and structurally-unusable (non-regular,
  over-cap) paths are stable failures with a long retry deferral; recovery is
  refresh-driven — the provider-artwork backfill sweep deliberately skips
  `file://` sources.

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
- A refresh that does not rediscover a slot's cached local copy (a `local/`
  key with a `file://` source) keeps it unless the sidecar's own folder answers
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
  with local metadata off for that level, during an Identify, or when the
  local provider's call failed, cached local art is stored artwork like any
  other and no library folder is probed.
- Localization: local art is language-neutral. Localization rows skip local
  candidates, and the read-side overlay (`internal/catalog/localize.go`) keeps
  base art served from a `local/` key (`artworkkey.IsLocal`), so a viewer in
  another language sees the same local art. A slot whose local copy is not
  cached yet still serves its earlier art, which a localized image may
  replace.

**Deployment constraint:** the host running the metadata image-cache processor
must mount the media libraries at the same paths as the scanner/metadata
worker, otherwise local artwork jobs fail until the mount is present.

## Series depth and mixed libraries

`SeasonsRequest`/`EpisodesRequest` carry local path context; `season.nfo`
supplies season name/plot, `<basename>.nfo` and the episode still (see above) supply
episode metadata/thumbs (`internal/metadata/nfo/series_depth.go`). **Naming
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

## Known limitations

- The admin image picker does not surface local art (automatic chain path
  only).
- Multi-part movies: in a folder holding multiple content groups, only
  `<basename>.nfo` and the stack base name are read; a differently named NFO
  there is not found.
- No NFO writing, no music/audiobook/ebook NFO (those ecosystems use
  OPF/metadata.json — future builtin providers via the same registry).
- Feature detection: the `nfo` capability's presence in
  `GET /api/v1/libraries/provider-defaults` (see
  `docs/architecture/v1-scope.md`).
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
