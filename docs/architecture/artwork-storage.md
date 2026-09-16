# Artwork storage

Artwork writes and cleanup use `internal/artworkstore.Store`. The store owns
its filesystem root or S3 bucket; callers use the existing logical artwork keys.

## Backends

`artwork.storage_backend` accepts `auto`, `local`, or `s3`. `auto` selects S3
when the public bucket is configured and local storage otherwise. The local
root defaults to `/var/lib/silo/artwork`; containers must persist that directory.
S3 is recommended when multiple hosts serve the same catalog.

Only API and integrated processes open artwork storage. Worker processes do not
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
`server_settings`, and startup refuses a store with a different identity. The
reconcile task certifies the same row after a manual sweep, and the storage
sweep scopes its cursor to it. Once recorded, the admin settings API rejects a
change to `artwork.storage_backend` with `409 artwork_storage_locked`, and
`GET /admin/server/status` reports `artwork_storage.locked` so the UI disables
the control. Moving artwork is a manual operation:

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
without a restart. Artwork probes are cached for 30 seconds.

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

Intro and credits markers that an external process places under
`markers/<file hash>.json` are read through the same store.

Profile avatars use private S3 whenever it is configured, preserving existing
uploads even when catalog artwork uses local storage. Without private S3,
avatars can use local artwork storage with signed delivery. A public artwork
bucket alone does not enable avatar uploads. Avatar URL generation does not
probe storage.
