# Blob storage

`internal/blobstore.Store` is the backend-neutral store for the blobs Silo owns.
The store owns its filesystem root or S3 bucket; callers use their own logical
keys.

Every blob Silo owns goes through it: artwork, branding assets, intro/credit
markers, chapter thumbnails, downloaded subtitles, diagnostic bundles, job
artifacts, and profile avatars.

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
| `collection-images/…` | collection artwork |
| `library-posters/…` | library posters |
| `chapter-images/…` | chapter thumbnails |
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
Diagnostics orphan cleanup deletes only keys shaped exactly like a bundle,
`diagnostics/<user id>/<report id>.tar.gz`, so artwork from a provider slugged
`diagnostics` is never swept.

Only the Assets store is wrapped to record the storage identity. When Operational
shares it, a first write through any caller records it. A private S3 bucket stays
outside that wrapper so its identity cannot become the catalog's assets location.
At startup, `blobstore.Open` records the configured private bucket under
`storage.operational_identity`, even when it is empty or was written by an older
release. A different configured private location fails startup. This row locks
the private settings independently of the assets identity; an administrator
changes the location through a managed transition.

## Backends

`artwork.storage_backend` accepts `auto`, `local`, or `s3`. `auto` selects S3
when the public bucket is configured and local storage otherwise. The local
root defaults to `/var/lib/silo/artwork`; containers must persist that directory.
Local objects are written `0644` and directories `0755`, including diagnostic
bundles and job artifacts when they share the root, so the data directory's
ownership and mount options are what keep them private. Owner-only modes and a
separate operational root are deferred to follow-up storage work.
S3 is recommended when multiple hosts serve the same catalog.

The setting keys keep their original artwork-era names. They select the backend
for every blob in the Assets store, not just artwork, and renaming them would
cost a migration and an upgrade hazard for no operator benefit. They do not
govern the operational store when a private bucket is configured, which owns
itself. An S3 backend with no private bucket leaves Operational nil, which is how
diagnostics and job artifacts detect that they have nowhere to write.

## The bucket-shaped API

Diagnostics, admin jobs, and catalog seed were written against S3 and pass the
bucket an object was written to, so a bucket change does not orphan it. They
keep receiving `*s3client.Client` directly on an S3 backend, unchanged. A local
backend supplies `blobstore.BucketAPI`, which accepts and ignores the bucket
argument, reports `"local"` as its bucket name, and normalizes not-found to each
caller's sentinel.

The bucket name has to be non-empty because readers treat an empty one as
"storage unavailable". `"local"` is recorded into `admin_jobs.artifact_bucket`
and `client_diagnostic_reports.blob_bucket` and handed back on read, where it is
ignored.

## Presigning and download URLs

Only S3 can mint a URL that authorizes itself off this server. `BucketAPI`
answers `ErrNoPresign`, and `SupportsPresign` lets a caller ask before offering
a feature that would always fail. Three consequences:

- **Diagnostic bundles** already streamed through the API host on `/api/v2`, and
  the `/api/v1` handler falls through to streaming when presigning fails. No
  change was needed.
- **Job artifacts** gained `GET /api/v2/admin/jobs/{id}/artifact`. It is
  authorized by a signed capability, not a session: the presigned URL it
  replaces authorized itself, and the web UI opens `download_url` in a new tab
  with no `Authorization` header. The capability is minted by
  `artworkurl.NewJobArtifactSigner` under its own domain, so an artwork URL
  cannot be replayed against it and a capability for one job does not open
  another's artifact. Every rejection answers 404, so the route never reveals
  whether a job exists. The frozen `/api/v1` job response only presigns, so
  `POST /api/v1/admin/catalog/export-jobs` keeps answering 503 on a store that
  cannot presign rather than queueing an export v1 cannot retrieve.
- **Seven-day public links** cannot exist without presigning. The API answers
  `409` and the job projection carries `public_link_supported` so the UI hides
  the action instead of offering one that always fails.

`GET /api/v2/admin/jobs/capabilities` reports both answers before a client
fetches a job, since each depends on the configured backend rather than the
release.

Once the capability on an artifact URL verifies, the caller has proven it was
given that URL, so only a genuinely absent job or artifact answers 404 from
there; unreachable storage answers 503 rather than reporting a download as
permanently gone.

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
local store, or the public endpoint, bucket, or key prefix for an S3 store. When
only `storage.operational_identity` is recorded, the private location locks and
the assets location stays free until the first write through Assets. The
private bucket is locked on either backend, because it owns the operational
store whatever the backend is; adding one to a local install would strand what
its root already holds. Its endpoint and key prefix are locked while a bucket is
configured, and the prefix compares as the store normalizes it, so `ops/` and
`ops` name the same location. An `auto` backend that resolved to local also
cannot gain a public bucket, because that would flip the resolution on restart;
an explicit `local` backend can. `GET /admin/server/status` reports
`artwork_storage.locked`, and `/api/v2` adds `artwork_storage.private_locked`
for the operational location. Endpoint scheme and host and bucket names compare
case-insensitively, and prefixes ignore slashes, so an edit that only restyles
a value saves directly. For a locked location, the admin settings page opens a
managed transition when the backend, local path, public S3 location, or private
S3 location changes. The setup wizard keeps locked location fields read-only.
Independently of the lock, an explicit `s3` backend without a public bucket is
rejected as invalid, since the store could not open on restart.

## Managed transitions

Administrators change a recorded local or S3 location through the managed
storage-transition API. `start_fresh` does not read the source;
`preserve_uploads` copies personal artwork uploads and downloaded subtitles when
the assets location changes, and profile avatars when the operational location
changes. When the assets location changes, provider artwork returns to its saved
provider URL under `start_fresh` and `preserve_uploads`. Cached NFO/sidecar
artwork is not copied by either policy; refresh metadata for affected libraries
after restart and artwork reconciliation. Backfill Metadata Images does not
process local sidecar sources. `migrate_all` copies all data from each source
location that changes. PostgreSQL catalog metadata is retained and the old
storage is never deleted automatically.

Admission serializes stage selection and job creation across API nodes. A lost
job-creation response retains the stage until a separate read confirms that no
job was admitted. Recovery cleanup clears only its own transition ID, so a late
finalizer cannot erase a newer transition.

Every API or integrated process takes a shared PostgreSQL advisory lock before
opening blob storage and keeps that session until process exit. Start briefly
checks exclusive ownership; the job takes it again before copying and holds it
through commit and exit. A node that joins while the job is queued makes the
job fail before copying. A node that arrives during a copy waits until the old
process exits, then loads the committed settings. If the owner exits before
commit, its session lock is released and a single restarted node can retry the
job. The admission session is detached from the connection pool so pool cleanup
cannot release it while old workers are still running.

An unexpected loss of that PostgreSQL session, such as a database restart or
failover, releases its advisory lock before the process may notice. Silo probes
the session every second. A node that holds only the shared lock rejoins on a
new session with a non-blocking shared acquire, so an ordinary database restart
does not stop it. While the database cannot be reached, its blob writes are
paused and reads keep serving. The node stops instead of rejoining when any
node holds the exclusive lock at that moment, or when the recorded storage
identities no longer name the stores it opened, which means a transition
committed and restarted while it was out. It then restarts onto the committed
settings. The rejoin check reads those identities under the settings mutation
lock, and the owner confirms its admission session inside the commit's
transaction, so a node cannot rejoin between a lost owner session and the
commit. A node that owned the transition stops as well, because its exclusive
lock went with the session. Writes may still occur between the loss and its
detection. This bound is operational, not an
atomic cross-node write fence; verify the source and target before retrying
after an admission-session failure.

Copy policies run an unfenced bulk pass followed by a full delta pass while
public and private source mutations are fenced. The delta pass re-enumerates
from the beginning. PostgreSQL checkpoint rows record each bulk-pass listing
fingerprint and its execution ID, then mark rows seen by the fenced pass. Receipt
writes are batched once per listed page and also flush after 256 MiB or ten
seconds. Every error and cancellation path makes a final bounded write with a
detached context, so verified work survives even when the job context has been
canceled. An incomplete fenced listing never runs orphan cleanup. The transition
does not retain a key-sized set in application memory or issue a database round
trip per object. During the fenced pass of the same execution, Silo can compare
reliable listed size, ETag, and modification-time values against those rows,
avoiding a second object read when all three are unchanged. Bulk passes never
take this shortcut. Local filesystem listings are deliberately excluded
from this shortcut because their synthetic ETag cannot distinguish every
same-size in-place rewrite. New or changed objects, stores without reliable
listing metadata, and every cross-process resume receive full source-and-target
digest verification. Orphan cleanup selects checkpoint rows not marked seen in
the fenced run in bounded pages, deletes each target object first, and only then
deletes its checkpoint row. It never deletes unrelated target objects or source
objects. Tests without a database retain an equivalent in-memory implementation.
The fences remain held after the settings commit until the process restarts,
and release on every pre-commit failure or cancellation. Copy policies reject
overlapping source and target namespaces, including targets that overlap the
opposite source role. Every policy rejects overlapping public and private S3
targets. Sentinel probes catch endpoint aliases that string identity comparison
cannot recognize. Before copying or committing, the queued job writes, reads
back, and deletes a small object at each changed destination. A failed probe or
cleanup stops the transition.

A transition handles two locations independently: the assets store and the
operational store. The operational location is the private bucket when one is
configured, the local root on a local backend without one, and nothing on an S3
backend without one, matching `blobstore.Open`. The assets copy never carries
`diagnostics/`, `catalog-seeds/`, or `profile-avatars/`; those follow the
operational copy, which runs only when the operational location changes.
`preserve_uploads` copies avatars there; `migrate_all` also copies diagnostic
bundles and job artifacts. Both copy policies copy downloaded subtitles with the
assets, in every direction.

Diagnostic and job-artifact rows record their bucket, and a local root records
`"local"`. After a `migrate_all` restart, boot recovery repoints rows naming the
old operational bucket to the new one, so rows move between local disk and
private S3 in either direction. Rows naming `"local"` are repointed under every
policy: an S3 reader would otherwise presign or delete against a real bucket
of that name. Their objects were not copied, so they read as missing. Rows
naming any other bucket are left alone.

Disabling S3 makes local disk the only location: a transition from an S3
backend to a local one also clears the private bucket, so the operational store
becomes the local root. The selected policy determines which source objects are
copied there. A local install keeps its private bucket unless the transition
changes it, and adding or removing a private bucket on a local install is a
private-only transition. Profile avatars never enter public S3, so copy policies
require private S3 when the source has operational storage and the target is S3.
Legacy shared operational buckets are split by ownership: `diagnostics` and
`catalog-seeds` move only with the operational copy under `migrate_all`. If one
source namespace is nested inside the other, the enclosing copy excludes that
subtree. Source overlap detection also probes endpoint aliases before the bulk
copy and before writes are fenced; both copy passes reuse that result. A failed
probe or sentinel cleanup stops the transition before copying or committing
settings. The final pass fences only the stores the transition copies from, so a
store whose data stays put keeps accepting writes. A local root is both the
assets and the operational store, and its writes wait on a single fence, which
the transition takes once.

Start applies the settings API's per-key rules to the request, so a transition
cannot commit an unknown backend or a relative local path. It rejects a target
whose normalized store identities equal the active ones before creating a job,
and it computes the preflight from those same identities. Clearing the private
bucket clears the rest of the private location with it. The commit writes the
location keys the copy verified; every other storage setting keeps its current
value, so a credential rotated while the copy ran is not reverted. When the
private location changes, the commit also rewrites `storage.operational_identity`
to the new bucket, or clears it when private storage is removed.

Each listed page of 250 keys copies through a pool of eight workers; receipts,
counters, and progress update under one lock, and the page's receipts flush
before its cursor advances. Job progress is reported at most every two seconds
rather than per object. Objects up to 8 MiB are read whole and written with
`Put`, which records the `silo-sha256` checksum S3 compares for the image
cache's reuse check; larger objects stream. In the fenced pass, an object this
run already copied or verified is accepted once its source digest still
matches, without reading the target again: only the transition writes there.

The settings commit records a restart-pending stage before the runner requests
restart. Catalog artwork reconciliation never runs against an uncommitted
target. Boot recovery is bounded: it verifies that the committed target is
active, relocates private artifact references, and repairs an interrupted job
receipt. After the HTTP listener starts, one API node takes a PostgreSQL
advisory lock and runs the managed catalog reconcile in the background with the
same durable checkpoint envelope as the manual reconcile task. Transient
failures retry in process with capped exponential backoff. Each attempt first
checks whether staged reconciliation exists, avoiding lock contention while
idle, then rereads the stage after acquiring the lock. Only the lock owner writes
running or retry state, and a waiting node can take over after the owner exits. A
committed-target identity mismatch is instead recorded as blocked and is not
retried. Unreadable staged state is omitted from configuration snapshots while
explicit recovery reads continue to report the error; unreadable active
credentials still fail configuration loading. An unreadable or undecodable staged
setting does not prevent the server from starting: boot logs the error, the health
API reports recovery as blocked, and new transitions remain disabled until the
setting is repaired. Throttled
progress, the last error, and `running`,
`waiting_retry`, or `blocked` recovery state are stored with the staged
transition and exposed through the admin source-health API. The manual reconcile
task takes the same lock and fails fast if a managed public reconcile is pending,
preventing concurrent catalog mutation or checkpoint writers. Branding
references are checked after the catalog sweep, so a branding failure does not repeat completed
catalog work. A fully verified `migrate_all` skips the catalog sweep but still
checks the small branding set. Recovery state is cleared only after all required
post-restart work finishes. A private-only change skips both public catalog and
branding reconciliation.

Artwork and S3 clients are process-lifetime dependencies: the configuration
watcher updates its live configuration snapshot but does not rebuild these
clients. Committing a transition therefore cannot introduce an unfenced client
in the old process. If the host has no restart callback or refuses the restart,
the runner leaves the source fences held, records that a manual restart is
required on the job, and the admin UI surfaces that instruction.

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

Profile avatars live in the operational store, so private S3 keeps existing
uploads and their presigned delivery even when artwork is local, and a local
backend serves them from the shared root with signed delivery. A public artwork
bucket alone does not enable avatar uploads: an S3 deployment without a private
bucket has no operational store, and uploads stay unavailable. Avatar URL
generation does not probe storage.
