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
  whole operation.

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

Sidecar images (`poster`/`folder`/`cover`, `fanart`/`backdrop`/`background`,
`logo`/`clearlogo`, and `<basename>-poster/-fanart/-logo` variants; extensions
`.jpg/.jpeg/.png/.webp`, 8 MiB cap, symlinked leaves rejected) are discovered
by the provider's `GetImages` (`internal/metadata/nfo/images.go`). Clients —
including jellycompat — always receive the normal presigned
`poster_url`/`backdrop_url`/`logo_url`; library files are never served
directly. The admin image picker (below) is the only API path that reads
sidecar images.

- Sources are recorded as `file://<absolute-logical-path>` in `*_source_path`
  columns and cached by the metadata image-cache processor under
  `local/{contentType}/{contentID}/{hash8}/{imageType}/...` (`hash8` = content
  hash; the discriminator sits before the imageType segment because variant
  clamping reads the parent directory). Editing the file and refreshing rotates
  the key; the stale hashed prefix is deleted after a successful re-cache, and
  item deletion trims local paths to the content prefix.
- The processor confines each read to the owning library's `media_folders`
  roots: a lexical check on the logical path, then a symlink-resolving re-check
  (both path and roots are resolved, so a legitimately symlinked root stays
  valid while an intermediate directory symlink escaping a root is rejected).
  Each library root is opened as an `os.Root` before the path is resolved, and
  the file is opened through the handle that is the same directory as the
  resolved root. A root or directory swapped for a symlink after that point
  cannot lead the open out of the library.
  Missing/unreadable/out-of-root and structurally-unusable (non-regular,
  over-cap) paths are stable failures with a long retry deferral; recovery is
  refresh-driven — the provider-artwork backfill sweep deliberately skips
  `file://` sources.
- Generic filenames attach only when the directory holds a single content
  group; a shared `folder.jpg` in a flat multi-movie folder applies to none.
  `<basename>-poster.jpg` style names always attach to their file.
- `applyBestImages`' final write has a local exemption so rating-0 local art
  can fill already-matched items and cannot be stickily displaced by remote
  art; the localization pass skips language-neutral local candidates.

**Deployment constraint:** the host running the metadata image-cache processor
must mount the media libraries at the same paths as the scanner/metadata
worker, otherwise local artwork jobs fail until the mount is present.

### Admin image picker

`GET /api/v2/admin/items/{id}/images` also offers a movie's or series' local
sidecar artwork. The built-in sidecar discovery runs with the item's media files
and sidecar directories, so it finds the same files a refresh would, and it runs
whether or not the library's metadata chain uses the NFO provider: choosing an
image is an explicit admin action. Local choices come only from that discovery;
a `file://` URL returned by any provider in the chain is dropped, so a provider
cannot get another library file offered as this item's artwork. Each local
choice has the provider ID `local`, keeps its `file://` path as `original_url`,
and shows a 300-pixel-wide WebP preview as an inline `data:` URI (the web client
authenticates with a bearer token, so an `<img>` cannot fetch an authenticated
preview route). The preview is read under the same root confinement, symlink and
size checks as the processor; a file that fails them is left out of the list,
and the list's `provider_errors` reports it under `local`. Only the movie and
series list does this: any other list treats a provider's `file://` URL as an
ordinary provider choice and never reads it.

`POST .../images/apply` with a `file://` `original_url` caches the file only
when the item's sidecar discovery offers it for that image type, reads it under
the same checks, stores it under the same `local/...` key the processor would
use, and publishes it with the images lock like any other choice. Seasons,
episodes and frozen v1 keep provider-only choices. The node serving the admin
API reads the file, so it needs the same library mounts as the processor;
without them local choices are simply not offered.

## Series depth and mixed libraries

`SeasonsRequest`/`EpisodesRequest` carry local path context; `season.nfo`
supplies season name/plot, `<basename>.nfo` + `<basename>-thumb.ext` supply
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

- The admin image picker offers local art for movies and series only, not
  for seasons or episodes.
- Multi-part movies: a basename-mismatched NFO in a folder holding multiple
  content groups is not found (directory candidates are suppressed there).
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
