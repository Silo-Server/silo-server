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
  add people (`internal/metadata/local_precedence.go`). Lists from two remote
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

## Identity: hint-first, never candidate-first

NFO `<uniqueid>` values (tmdb/imdb/tvdb) are **trusted identity hints** seeded
before remote search (`IdentityHintProvider`, applied in
`internal/metadata/service.go`), unlocking full remote enrichment even when
search-by-title fails. Conflict policy by mode:

- initial match / scheduled refresh: NFO hints beat folder-name hints; stored
  durable IDs beat NFO hints (no background identity flips);
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
path-deterministic `local:` content id; adding a `<uniqueid>` later promotes it
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
- Multi-part movies: a basename-mismatched NFO in a folder holding multiple
  content groups is not found (directory candidates are suppressed there).
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
