# S3 Provider Change: Artwork Cache Reconciliation Design

Commands and paths in this document assume the repository root is the cwd.

## Goal

When an admin changes the public S3 provider (endpoint, bucket, or key prefix), cached
artwork must heal automatically. Today the change silently breaks every cached image
forever: the database keeps pointing at object keys that only exist in the old bucket,
the caching pipeline permanently skips already-cached rows, and clients eat the 404s
directly from S3 so the server never even observes the breakage.

Design goals, in priority order:

1. **Users never see broken images.** During and after a provider change, every image
   either serves from the new bucket, falls back to its provider source URL, or falls
   back to its thumbhash placeholder / generated artwork.
2. **The admin makes no decisions and cannot get it wrong.** No "did you migrate your
   data?" dialog. Silo verifies reality and does the right thing for migrated,
   unmigrated, and partially migrated buckets alike.
3. **No wasted work.** An admin who migrated their objects to the new bucket must not
   trigger a full catalog re-download (provider rate limits, bandwidth, time).

## Background: how caching works today

- The image cache pipeline (`internal/imagecache`, `internal/metadata/image_cache_processor.go`)
  downloads provider artwork, generates variants (`original`, `w500`, `w300`, ...), and
  uploads them to the **public** S3 bucket under bucket-relative keys such as
  `tmdb/movies/550/poster/original.webp`.
- On success, the target row's path column (`media_items.poster_path`,
  `episodes.still_path`, `people.photo_path`, ...) is rewritten from the provider URL to
  the cached relative key. The original provider URL is preserved in the matching
  `*_source_path` column. Thumbhashes are stored in their own columns and are
  content-derived, so they remain valid across a re-cache of the same source.
- The recurring `cache_metadata_images` task (60s interval,
  `internal/taskmanager/tasks/cache_metadata_images.go`) calls
  `EnqueueExistingProviderArtwork` (`internal/metadata/image_cache_job_repo.go`), which
  only enqueues rows whose destination path is still a provider URL (`LIKE '%://%'`) or
  empty. **The cached path in the row is the durable dedup marker** — once set, the row
  is never enqueued again.
- Serving: stored relative keys are resolved to presigned / read-endpoint URLs against
  the *current* S3 config at request time (`internal/metadata/image_resolver.go`,
  `PresignURLWithExpiry` in `internal/catalog/detail.go`). Absolute `http(s)://` paths
  pass through to clients unchanged. Clients then fetch from S3 directly.
- All `s3.` settings keys are restart-required (`internal/config/restart_keys.go`), so a
  provider change only takes effect at the next server start.

Consequences of a provider change with no migration: every resolved URL points into the
new bucket where nothing exists → broken images; nothing re-enqueues; recovery requires
hand-written SQL. If the admin *did* migrate objects, everything keeps working — which is
why a blind "wipe and re-cache on settings change" would be strictly worse than today.

## Approach overview

Three pieces:

1. **Storage identity fingerprint** persisted in `server_settings`. At startup a new
   reconcile task compares the fingerprint against the live config; a mismatch means the
   storage identity changed and a verification sweep is needed.
2. **`reconcile_artwork_cache` task** (task manager, startup trigger + manually
   runnable). Probes the new bucket, verifies cached objects, and resets rows whose
   objects are missing so the existing pipeline rebuilds them. Recovery action depends on
   the image's source scheme (see table below).
3. **Admin messaging**: an informational note when saving changed S3 settings, live task
   progress in the existing Tasks UI, and a completion notification summarizing what was
   verified, re-queued, and unrecoverable.

There is deliberately **no new serving logic**: resetting a path back to its provider
source URL restores the pre-cache pass-through behavior, so images render (hotlinked)
immediately and flip back to cached S3 URLs as the queue drains.

## Storage identity fingerprint

- Value: normalized `endpoint|bucket|key_prefix` of the **public write** config
  (`cfg.S3.Public.Endpoint`, `.Bucket`, `.KeyPrefix`), lowercased, trimmed, stored as a
  plain (non-encrypted) `server_settings` row, e.g. key `s3.public_storage_identity`.
  The read endpoint and URL-auth settings are excluded on purpose: changing how objects
  are *served* (CDN, token auth) does not move the stored data.
- Bootstrap: if no fingerprint row exists (first run after upgrade), adopt the current
  identity without reconciling. Admins who changed providers before this feature existed
  can run the task manually.
- The fingerprint is updated to the new identity **only after a sweep completes**. An
  interrupted sweep therefore re-triggers at the next startup; the sweep is idempotent.

## The `reconcile_artwork_cache` task

Task manager registration: key `reconcile_artwork_cache`, category Metadata, visible,
default trigger `TriggerTypeStartup`, manually runnable from the Tasks UI. On startup it
compares fingerprints and exits immediately (fast no-op) when they match. A manual run
skips the fingerprint check and always sweeps — this doubles as disaster recovery for
"my bucket was wiped / partially lost" with no provider change at all.

### Phase 1 — probe

The probe runs first (before any counting): HEAD (via the existing
`s3client.Client.ObjectExists`) a sample of ~200 stored cached keys across all
surfaces, taken with plain `LIMIT` sampling — `ORDER BY random()` would full-scan and
sort every surface, and the probe only has to answer "does this bucket hold the cache
at all". Because the DB stores the full key of the `original` variant (e.g.
`tmdb/movies/550/poster/original.webp`), the stored path is exactly the key to check —
no guessing. Progress-denominator `count(*)` queries run only in verify mode; bulk
mode reports `RowsAffected` and never pays for counts. Probe errors are reported but
tracked separately (`sweep_errors` vs `errors`) so they cannot consume the sweep's
error budget. Two reliability guards protect bulk mode from a degraded probe: a run
aborts outright when more than half the probe requests error (an outage is not a
miss), and bulk reset requires a minimum number of *successful* samples
(`artworkReconcileBulkMinSample`) — a probe thinned out by transport errors takes the
safe per-row path instead.

- **≥95% missing** → "bulk reset" mode: skip per-row verification and reset all cached
  rows by SQL alone (the data plainly was not migrated). The threshold is not 100% so a
  handful of coincidentally-present keys cannot force millions of pointless HEADs.
- **All present** → data was migrated; run the full sweep anyway (it is one cheap HEAD
  per image and catches partially-missed prefixes) but expect near-zero resets.
- **Mixed** → full per-row sweep.

### Phase 2 — sweep and reset

Iterate every surface that stores cached public-bucket keys, in batches with keyset
pagination and a bounded HEAD concurrency (`artworkReconcileHeadWorkers`, 16 in
flight, each attempt under its own timeout), reporting progress
through the task's `ProgressReporter`:

| Surface | Cached columns | Recovery when object is missing |
| --- | --- | --- |
| `media_items` | `poster_path`, `backdrop_path`, `logo_path` | Reset column to its `*_source_path` when the source is a re-downloadable provider URL |
| `media_item_localizations` | `poster_path`, `backdrop_path`, `logo_path` | Same |
| `seasons`, `season_localizations` | `poster_path` | Same |
| `episodes` | `still_path` | Same |
| `people` | `photo_path` | Same |
| Chapter thumbnails (`media_files.chapters` JSONB) | per-chapter `thumbnail_path` | Clear path + thumbhash + retry state; the 6-hour `chapter_thumbnail_backfill` task regenerates from the media file |
| Collections (`library_collections`, `user_personal_collections`) | `poster_url` / `backdrop_url` | Clear (plus `poster_auto_generated` / `poster_from_template` flags) → UI falls back to the generated collage/poster; report. The original template reference is not stored, so template posters are not re-uploaded automatically |
| Server branding (`internal/branding`) | `server_settings` refs (`branding.*_ref`) | Verified by `branding.Service.ReconcileMissingAssets`; missing refs cleared → built-in defaults; report |
| Library artwork (`media_folders.poster_path`) | `library-posters/{id}.ext` | Clear → default tile; report (no auto-refill) |
| Audiobook / ebook covers (`local/...` keys in `media_items`) | `poster_path` | Clear + null `last_refreshed`; the enrichment sweeps re-extract embedded covers |

Profile avatars are stored in the **private** bucket (`deps.S3Private`), not the public
one, and are therefore out of scope for this reconcile.

Reset semantics for provider-sourced artwork (the overwhelming majority):

- Single guarded UPDATE per row/column:
  `SET poster_path = poster_source_path WHERE content_id = $1 AND poster_path = $2` —
  the `poster_path = $2` guard makes the sweep safe against concurrent metadata
  refreshes (lost-update protection).
- **Thumbhash columns are left untouched.** Placeholders keep rendering throughout.
- Retained `metadata_image_cache_jobs` rows need no special handling:
  `EnqueueExistingProviderArtwork` calls its upsert with `requeueSucceeded=true`, which
  flips even `succeeded` rows back to `queued`. (An earlier draft of this spec called
  for deleting job rows; implementation showed it is unnecessary.)
- Sources with schemes `upload://`, `s3://`, `file://`, `local://`, `generated://` are
  never "reset to source" — those rows are cleared instead, and clearing `media_items`
  artwork also nulls `last_refreshed` so the book-enrichment sweeps (which require
  `last_refreshed IS NULL`) re-extract embedded covers.

### Phase 3 — rebuild (existing machinery, no new code)

Nothing re-downloads inside the reconcile task itself. After reset, the 60-second
`cache_metadata_images` loop sees provider URLs in destination columns again, enqueues
them, and the processor re-caches into the new bucket. The processor's per-variant
`ObjectExists` checks (`internal/imagecache/imagecache.go`) make rebuilds idempotent and
partial-migration-safe: variants that survived migration are skipped, missing ones are
uploaded.

### Phase 4 — report and finalize

- Update the fingerprint row to the new identity — but only when the sweep completed
  with zero sweep errors. Skipped-on-error rows were never verified, so a run with
  sweep errors reports its partial results (applied resets are durable) and leaves
  the fingerprint stale for the next startup to retry.
- Task completion message: verified / re-queued / cleared counts, plus structured
  result data (JSON) persisted with the execution record for the Tasks UI.
- No durable inbox notification: the notifications system has no free-form admin
  message type (deliveries are content-keyed and per-profile), and adding one for this
  feature is not worth the payload-renderer plumbing. The persisted task result plus
  warn logs carry the report.
- Log unrecoverable items individually at `warn` with content IDs so an admin can
  re-upload the specific artwork that was lost.

## Admin UX

1. Admin edits S3 settings → existing connection check (`admin_settings_checks.go`)
   validates the new provider before saving, as today.
2. When the saved values change the storage identity, the settings UI adds one
   informational callout next to the existing restart-required notice:
   *"Artwork is cached in object storage. After restart, Silo verifies the cache against
   the new storage and automatically re-caches anything missing. Uploaded artwork
   (custom posters, avatars, branding) cannot be re-downloaded — migrate your bucket
   contents if you want to keep them."*
   No confirmation dialog, no migration question.
3. After restart the reconcile task appears in the Tasks UI with live progress, and
   finishes with the summary + notification described above. It can be re-run manually
   at any time.

## End-user UX

- Before the sweep touches a row: broken S3 URL, but the thumbhash placeholder renders.
  The in-memory resolved-URL cache is empty after the (required) restart, so no stale
  presigned URLs outlive the config change.
- After reset: the API serves the provider's original URL via the existing absolute-URL
  pass-through — image present immediately, just hotlinked and un-resized.
- After re-cache: back to normal S3-served variants. Users should not be able to tell a
  migration happened.

## Failure modes

- **New provider unreachable at startup**: probe HEADs fail with errors (not
  "missing"). The task must distinguish `ObjectExists == false` from a transport error
  and abort without resetting anything, leaving the fingerprint stale so it retries next
  startup / next manual run. Never mass-reset on the basis of errors.
- **Sweep interrupted** (restart, crash): fingerprint was not updated; the task re-runs
  at next startup. Already-reset rows are simply in the "provider URL" state the enqueue
  loop handles; already-verified rows get re-HEADed. Idempotent.
- **Key-prefix-only change**: `ObjectExists` resolves keys under the current prefix, so
  objects under the old prefix correctly count as missing and re-cache under the new
  prefix.
- **Provider rate limits during rebuild**: unchanged from any large-catalog initial
  cache; the existing job queue's retry/backoff behavior applies.

## Out of scope

- The **private** S3 bucket (subtitles, downloads, transcode artifacts). A private-
  provider change has a different blast radius and mostly regenerable content; it
  deserves its own pass. This spec's fingerprint/task mechanism is deliberately reusable
  for it later.
- An S3→S3 object migration tool. Admins who want to keep uploaded artwork migrate with
  standard tooling (`rclone`, `aws s3 sync`); Silo's job is to heal what it can and
  report what it cannot.
- Serve-time 404 fallback. Clients fetch from S3 directly via presigned/read-endpoint
  URLs, so the server never sees the misses; a proxying fallback would put every image
  request through the server and is rejected on performance grounds.

## Implementation notes (as built)

- `internal/metadata/artwork_reconcile.go` — `ArtworkCacheReconciler`: table-driven
  sweep surfaces, probe, bulk/verify modes, and the bespoke chapter-thumbnail sweep.
  Small "precious upload" tables (collections, library posters) are marked
  `alwaysVerify` so a bulk reset can never blind-clear an upload that survived
  migration.
- `internal/taskmanager/tasks/reconcile_artwork_cache.go` — task registration,
  `ArtworkStorageIdentity` fingerprint helper, and the `ShouldRun` gate
  (`ScheduledConditionalTask` suppresses the startup trigger while the fingerprint
  matches; manual runs always sweep).
- `internal/branding/service.go` — `ReconcileMissingAssets` verifies and clears the
  four branding refs; composed into the task rather than the reconciler so `metadata`
  does not import `branding`. It runs *after* the fingerprint is certified and its
  errors are non-fatal (reported in the task message): a transient failure on a
  4-object check must never discard a completed catalog sweep and force it to repeat.
- Fingerprint lives in `server_settings` under `s3.public_storage_identity`
  (plaintext; encrypted values are GCM-bound to their key name, which complicates any
  future rename for zero benefit). It is seeded with `SetIfAbsent` at wiring time in
  `cmd/silo`, so first boot adopts the current identity without a sweep. Normalization
  mirrors real S3 semantics: endpoint/bucket are case-folded, but the key prefix keeps
  its case (S3 keys are case-sensitive — a case-only prefix edit is a real move) and is
  slash-trimmed exactly like `s3client.NormalizeKeyPrefix` applies it.
- The task manager's conditional-task preflight fails **closed**: a `ShouldRun` error
  skips the run (and the task's `ShouldRun` retries transient settings reads), so a
  startup DB blip cannot fail open into a full catalog sweep.
- No schema migration: the fingerprint is a `server_settings` row and all reset
  operations use existing columns.
- `s3client.Client` prepends `KeyPrefix` internally, so the reconciler passes DB-stored
  logical keys straight to `ObjectExists`.
- Web UI: conditional callout in
  `web/src/pages/admin-settings/StorageSettings.tsx`, shown while any of
  `s3.public_endpoint` / `s3.public_bucket` / `s3.public_key_prefix` is dirty.
