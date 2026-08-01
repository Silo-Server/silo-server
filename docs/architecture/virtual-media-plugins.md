# Virtual media plugin integration

Silo supports storage-free catalog entries through an additive RuntimeHost
contract. Plugins submit validated metadata and provider-neutral `virtual://`
URIs; the host
owns persistence, indexing, metadata refresh, authorization, and playback.

## Trust boundary

- Plugins never receive PostgreSQL credentials and never depend on Silo table
  layouts.
- `RuntimeHost.UpsertVirtualMedia` derives the caller's installation identity
  from the authenticated plugin process. It does not accept an installation ID
  from plugin input.
- The host validates media type, destination library, external identity, and
  URI scheme before opening a transaction.
- Registration is idempotent. Stable provider IDs select the canonical item,
  while a URI-level advisory lock prevents duplicate virtual files.
- Search-index events commit in the same transaction. Metadata refresh and
  home-section cache invalidation run through normal Silo services afterward.

## Playback lifecycle

1. A request-router plugin registers a movie or already-aired series episodes.
2. Silo stores virtual `media_files` with `container=virtual`; no placeholder
   files are created.
3. Playback planning recognizes a virtual source without filesystem probing.
4. At playback time Silo asks the owning plugin to resolve the virtual URI.
5. Direct-compatible sources are proxied through Silo with byte-range support.
   Sources requiring conversion continue through Silo's normal capability-aware
   remux/transcode path. Provider credentials never enter client responses,
   durable plans, or transcode recipes.

The resolver runs at playback time so signed upstream URLs are not persisted.

## Administrator setup

The server administrator installs a Silo build containing this host contract,
then installs a compatible virtual-library plugin. The plugin configuration
contains a compatible Stremio `manifest.json` URL, movie library ID, series library ID, an
optional TMDB credential, and a durable monitored-queue path. Database access
is neither requested nor supported.

## Updating from upstream

The feature is intentionally isolated to an additive SDK RPC, the catalog
registrar, RuntimeHost wiring, and narrow playback-source handling. To update:

```sh
git fetch upstream
git rebase upstream/main
go test ./internal/catalog ./internal/pluginhost ./internal/playback ./internal/requests ./internal/sections ./internal/plugins ./internal/api/handlers ./cmd/silo
go vet ./internal/catalog ./internal/pluginhost ./internal/playback ./internal/requests ./internal/sections ./internal/plugins ./internal/api/handlers ./cmd/silo
docker build -t silo-server:virtual-playback .
```

Commands assume the repository root is the cwd. If upstream publishes the
RuntimeHost contract in a newer SDK release, update `go.mod`, remove the local
SDK compatibility replacement, regenerate no code locally, and rerun the same
contract and playback tests.
