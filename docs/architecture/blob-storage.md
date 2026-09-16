# Blob storage

`internal/blobstore.Store` is the backend-neutral store for the blobs Silo owns.
The store owns its filesystem root or S3 bucket; callers use their own logical
keys.

Artwork, branding assets, intro/credit markers, chapter thumbnails, and
downloaded subtitles go through it today. Diagnostic bundles and job artifacts
still use their own bucket-oriented code in `internal/diagnostics` and
`internal/adminjob`, and still require S3; they move onto the store in a later
change. The prefixes reserved for them are listed below so the namespaces do not
have to be renegotiated when they do.

## The two stores

`blobstore.Open` returns a `Stores` pair:

- **Assets** — artwork, branding assets, intro/credit markers, chapter
  thumbnails, and downloaded subtitles. Carries the recorded storage identity.
- **Operational** — diagnostic bundles, job artifacts, and profile avatars.

These are separate because the public bucket can serve browsers directly under
token auth and the private one never does. A configured private bucket owns the
operational store whatever the backend is: avatars have always lived there, and
moving artwork to disk must not strand the `profile-avatars/` keys an install
already uploaded. Only a local backend with no private bucket puts both in one
root, where the key prefixes each caller already uses keep the namespaces apart:

| Prefix | Owner |
|---|---|
| `<provider>/<kind>/<id>/<imageType>/…` | artwork (`internal/artworkkey`) |
| `branding/…` | branding assets |
| `markers/…` | intro and credit markers |
| `subtitles/…` | downloaded subtitles |
| `diagnostics/…` | diagnostic bundles |
| `catalog-seeds/…` | admin job artifacts |
| `profile-avatars/…` | profile avatars |

Artwork keys start with a metadata provider segment, so they do not collide with
the reserved prefixes. That is convention rather than enforcement:
`PluginProvider.Slug()` returns a plugin's capability ID unvalidated, so a
metadata plugin whose ID is one of those names would write into that namespace.
The same hazard already existed when artwork and subtitles shared the public
bucket.

Nothing walks a store root unbounded. The artwork sweep names its prefixes
explicitly and refuses an empty one, because `parseArtworkObjectKey` accepts any
`a.b.c` filename and would read a bundle name as a revisioned variant.

Only the Assets store is wrapped to record the storage identity. When Operational
shares it, a first write through any caller records it. A private S3 bucket is
deliberately left unwrapped: recording its identity would name it as the
catalog's assets location and refuse the real assets store on the next start.

## Backends

`artwork.storage_backend` accepts `auto`, `local`, or `s3`. `auto` selects S3
when the public bucket is configured and local storage otherwise. The local
root defaults to `/var/lib/silo/artwork`; containers must persist that directory.
S3 is recommended when multiple hosts serve the same catalog.

The setting keys keep their original artwork-era names. They select the backend
for every blob in the Assets store, not just artwork, and renaming them would
cost a migration and an upgrade hazard for no operator benefit. They do not
govern the operational store when a private bucket is configured, which owns
itself. An S3 backend with no private bucket leaves Operational nil, which is how
diagnostics and job artifacts detect that they have nowhere to write.

Only API and integrated processes open blob storage. Worker processes do not
probe it or compare the catalog's recorded backend with their local settings.
Startup probes the selected backend with a five-second timeout. Temporary storage
failures allow the process to start with degraded readiness; invalid paths and
backend mismatches remain startup errors. Readiness repeats the probe at most
once every 30 seconds, independently of a caller disconnecting. A local probe
writes, syncs, and removes a temporary file.

## Store contract

`Put` atomically overwrites an object. `Get` and `Stat` return object size,
modification time, and a quoted ETag. Missing objects return `ErrNotFound`.
`Delete` counts absent keys as deleted, and prefix deletion removes a subtree.
Listings use lexical keys and a cursor equal to the last returned key. Local
pagination skips completed subtrees and stops after a page plus one object;
each visited directory's entries are read and sorted in memory.

Keys are relative, non-empty, and at most 1024 bytes. Empty segments, dot
segments, backslashes, and control characters are rejected. Local storage
refuses symlinks and non-regular files below its root. MIME types come from key
extensions. The `.tmp-` and `.probe-` filename prefixes are reserved. Listings
reclaim abandoned temporary files older than 24 hours; readiness also cleans
temporary files in the root. Cleanup preserves files locked by active writers.

## Storage identity

Every store reports an `Identity()`: `local|<absolute root>` or
`s3|<endpoint>|<bucket>|<key prefix>`. It names where objects live and nothing
about how they are read, so changing a public read endpoint never counts as a
move. The first successful write records it as `artwork.storage_identity` in
`server_settings`, and startup refuses a store with a different identity. Only
the scheme and host of an S3 endpoint are case-insensitive; an endpoint path
and the key prefix keep their case. Releases before the identity row
lowercased the whole endpoint, so startup accepts a recorded S3 identity whose
endpoint equals the configured one lowercased, with the bucket and key prefix
matching exactly, and rewrites the row in the exact form.
The reconcile task certifies the same row after a manual sweep, and the storage
sweep scopes its cursor to it. Once recorded, the admin settings API rejects
any write that would resolve to a different identity with
`409 artwork_storage_locked`: a different backend, `artwork.local_path` for a
local store, or the public endpoint, bucket, or key prefix for an S3 store. An
`auto` backend that resolved to local also cannot gain a public bucket, because
that would flip the resolution on restart; an explicit `local` backend can.
`GET /admin/server/status` reports `artwork_storage.locked` so the UI disables
the control. Independently of the lock, an explicit `s3` backend without a
public bucket is rejected as invalid, since the store could not open on
restart. Moving artwork is a manual operation:

1. Stop artwork writers.
2. Copy the artwork tree to the new store, preserving logical keys.
3. Update the backend configuration in the database directly.
4. Delete the `artwork.storage_identity` row and restart.

This guard does not migrate data. There is no portability format, storage
health state machine, generation marker, or mount sentinel. Existing revision
tracking, reconciliation, and garbage collection continue to own lifecycle.

## Readiness

`/ready` fails only when PostgreSQL is unreachable. A failed artwork or S3
probe answers 200 with `"status":"degraded"` and the same per-dependency
booleans the error shape carries, so a storage outage is visible without
removing the node from service: the API keeps answering,
artwork routes return 503 on their own, and readiness follows storage recovery
without a restart. Artwork probes are cached for 30 seconds. This changes the
retained `/api/v1/ready` contract, which previously answered 503 on an S3
`HeadBucket` failure; the contract document records the new behavior.

## Delivery

Local URLs use an HMAC derived from the JWT secret and the fixed domain
`silo-artwork-url-v1`. The signature covers `artwork-v1`, the logical key, and
the expiry. URLs stay stable within issuance buckets of 15 minutes, reduced to
the TTL for shorter URLs. Their remaining lifetime is at least the configured
TTL, with up to one bucket added. Invalid or expired capabilities return 404 so
the route does not reveal whether a key exists.
Revisioned URLs are cacheable for their remaining lifetime and marked immutable;
mutable uploads use private caching. S3 installations continue to use direct
presigned or public URLs.

Local storage publishes each object with an atomic rename, and direct S3 reads
see an object as soon as its upload returns, so catalog responses resolve the
manifest key they hold and a missing object answers 404 and enqueues repair.
Only external delivery (a public or token-authenticated read endpoint in front
of S3) can lag behind a write. That configuration alone runs the
`verify_artwork_delivery` task and consults the verified-keys manifest when
choosing which variant to advertise.

Local URLs are root-relative, which is enough for clients of the API listener
and for the Jellyfin and Audiobookshelf compatibility listeners, which mount
the same signed artwork route so their cover redirects resolve on their own
port. Consumers outside the server, such as Discord embeds, anchor them to
`server.public_url` and send no image when it is unset.

Local storage publishes an object by writing to a temporary file, syncing it,
renaming it into place, and syncing the containing directory, so a crash after
`Put` returns cannot leave the catalog referencing a key the store does not
show.

Intro and credits markers that an external process places under
`markers/<file hash>.json` are read through the same store.

Profile avatars use private S3 whenever it is configured, preserving existing
uploads even when catalog artwork uses local storage. Without private S3,
avatars can use local artwork storage with signed delivery. A public artwork
bucket alone does not enable avatar uploads. Avatar URL generation does not
probe storage.
