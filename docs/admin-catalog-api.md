# Catalog administration API

Catalog source discovery uses acting-administrator authorization. These endpoints
expose server paths and storage keys intentionally for catalog administration.
Ordinary profiles cannot use them.

| Endpoint | Result |
| --- | --- |
| `GET /api/v2/admin/catalog/import-sources` | One storage page, filtered to catalog seed bundles |
| `GET /api/v2/admin/catalog/local-import-sources` | Local catalog seed files, ordered by path |
| `GET /api/v2/admin/filesystem/browse` | Subdirectories with resolved `path` and `parent` |

All three accept `limit` (1–200, default 50) and an opaque `cursor`. Results have
`items` and `page`, including `has_more` and an optional `next_cursor`. Cursors are
bound to the operation and caller; filesystem cursors also bind the requested path
and name prefix. Changing those filters starts a new listing.

Storage discovery makes one bounded S3 listing request per API request. A page can
contain no matching bundles and still have `has_more: true`; clients must retain
its continuation cursor. Storage order follows the provider's listing order,
not modification time. Local listings use ascending path order. The web offers
explicit continuation controls and does not drain every page in the background.

Filesystem browsing accepts `path` (an absolute path, defaulting to the filesystem
root) and optional `name_prefix` (case-insensitive). Prefix filtering happens
before pagination so autocomplete can find folders outside the first unfiltered
page. Directory symlinks are followed; broken links and ordinary files are omitted.
Local seed discovery accepts regular `.json.gz` files and links to those files.

Local directory enumeration reads fixed-size batches and retains only a bounded
page of candidates. It still scans the directory to establish ordering. Results
reflect the responding server's filesystem and are not a snapshot across pages;
concurrent additions and removals may change later pages. A missing configured
local seed directory returns an empty listing; a missing explicitly browsed
directory returns not found.

These are web administration utilities. The Apple and Android clients have no
callers for them, and the Jellyfin compatibility surface has no equivalent catalog
seed or administrator filesystem operation.

## Export and import

| Endpoint | Acknowledgment |
| --- | --- |
| `POST /api/v2/admin/catalog/export` | `200` with synchronous gzip bytes |
| `POST /api/v2/admin/catalog/export-jobs` | `202` after a queued export job is persisted |
| `POST /api/v2/admin/catalog/import` | `200` after the catalog import transaction commits |
| `POST /api/v2/admin/catalog/import-jobs` | `202` after a queued import job is persisted |
| `POST /api/v2/admin/catalog/export-jobs/{id}/publish` | `200` with a saved signed download URL, or `409` when storage cannot presign |
| `GET /api/v2/admin/jobs/capabilities` | Whether artifact download and public links are supported |
| `GET /api/v2/admin/jobs/{id}/artifact` | Streams a completed job's artifact to a signed capability |
| `GET /api/v2/admin/catalog/search/status` | Current catalog search runtime status |

Export requests use JSON `{}` for the entire catalog or `library_ids` containing
opaque string IDs. Direct export declares `application/gzip` as its success
representation; request it with an appropriate `Accept` header. A JSON-only
`Accept` is refused with `406`. Errors still use `application/problem+json`.
Other operations retain JSON negotiation. Large exports should use background
jobs, whose exporter writes through a temporary file before storage upload.

Import requests use JSON with exactly one of `local_path`, `export_job_id`,
`artifact_key`, or `remote_url`, plus `conflict_mode` (`skip_existing` or
`overwrite_existing`) and `path_rewrites` (an array of `from`/`to` pairs, possibly
empty). Null fields and incomplete rewrite pairs are rejected before execution.
Missing root rewrites produce a validation problem with `path_rewrite_required`
field errors that explain the affected source roots.

Direct import runs the existing catalog transaction and returns its committed
counts. If a response is lost, callers must inspect the catalog before deciding
whether to submit again. The web explicitly offers background execution or
"Import and wait"; it does not fall back between them after an error. All transfer
mutations disable automatic mutation and authentication retries.

Queued responses contain the existing typed administrator job and a `Location`
pointing to `GET /api/v2/admin/jobs/{id}`. An active job of the same kind produces
`409`; when available, `Location` identifies that active job. Persisting a job
acknowledges scheduling, not import/export completion or exactly-once execution.
The worker later opens the local path, downloads the remote URL, or reads the
storage object. Source bytes are not frozen at submission. Local paths must be
available on the worker that claims the job; remote content may change between
submission and execution.

The publish operation requires a completed export artifact. It saves a signed URL
with a seven-day signature lifetime and returns `job_id`, `url`, and `expires_at`.
It does not change the storage ACL. Repeating it returns the saved URL and original
expiry, even after expiry; it does not renew the link. Artifact retention or
removal can make the link unavailable before its signature expires.

Publishing needs object storage that can presign. A server storing artifacts on
local disk cannot produce a URL that works off the server, and publish answers
`409 conflict` there. Read `public_links` from
`GET /api/v2/admin/jobs/capabilities` before offering the action; the same answer
appears per job as `public_link_supported`.

## Artifact download

`GET /api/v2/admin/jobs/{id}/artifact` streams a completed job's artifact as
`application/gzip`. It is authorized by the `exp` and `sig` capability carried in
the job's `download_url`, not by a session, so the URL works in a browser tab
that sends no `Authorization` header — the same property the presigned storage
URL has. The capability is scoped to one job, expires with `download_expires_at`,
and answers `404` when it is absent, invalid, expired, issued for another job, or
when the job or artifact does not exist. Storage that is configured but
unreachable answers `503`.

Read `download_url` from the job rather than constructing this URL. An S3-backed
server returns a presigned storage URL there instead, and both forms are opaque.

Search status retains the existing runtime/provider/index information, uses UTC
millisecond instants, and represents `last_processed_event_id` as a string. Its
task links and media-type coverage groups are finite collections. These transfer
and status operations have no Apple, Android, or Jellyfin compatibility callers.

## Literary editions and matches

Literary administration uses acting-administrator authorization and the existing
literary work service. These routes remain registered when the service is absent
and return `503` until it is available.

| Endpoint | Result |
| --- | --- |
| `GET /api/v2/admin/literary-works/items/{content_id}/candidates` | Bounded ranked `candidates` |
| `POST /api/v2/admin/literary-works/link` | Selected `work_id` |
| `POST /api/v2/admin/literary-works/matches/confirm` | `status: "ok"` and `work_id` |
| `POST /api/v2/admin/literary-works/matches/ignore` | `status: "ok"` |
| `DELETE /api/v2/admin/literary-works/{work_id}/items/{content_id}` | Empty `204` |

Candidates accept `limit` from 1 to 100, default 20. This is a top-ranked result
set, not a paginated full inventory. The existing scorer filters candidates and
orders by descending score, breaking ties by target content ID. Each result
contains source and target content IDs, optional target work ID, score, link
source, and a string-valued evidence object. Empty evidence is `{}`.

Link accepts 1–100 nonempty `content_ids` and an optional `work_id`. Without a
work ID, the service reuses an existing linked work or creates one. Confirm and
ignore accept distinct `source_content_id` and `target_content_id`. Decisions
are attributed to the acting account's user ID, not a household profile ID.

Mutations return after their existing service calls finish. Confirmation first
links editions and then records the decision in a separate write; failure of
that write can leave the editions linked. There is no whole-operation transaction
or durable request replay receipt. All four mutations are non-retryable: after
an uncertain result, inspect current state before deciding whether to submit again.
These administration routes have no existing web, Apple, Android, or Jellyfin
compatibility callers. The viewer literary-work endpoint remains unchanged.

## Person curation

`POST /api/v2/admin/people/{id}/refresh` waits for the existing provider refresh
(up to its two-minute deadline) and returns the typed `Person`. It differs from
the viewer refresh operation, which queues work. A missing person returns `404`;
a provider without person metadata returns a `503` dependency problem. The frozen
v1 response for that provider failure remains `502` with `provider_error`.

`PATCH /api/v2/admin/people/{id}` accepts optional `name`, `bio`, `birth_date`,
`death_date`, `birthplace`, `homepage`, `tmdb_id`, `imdb_id`, and `tvdb_id`.
Omission or null preserves the existing value. Empty strings clear values;
nonempty dates must use `YYYY-MM-DD`. Validation precedes persistence. Success
returns the updated `Person`, with its ID represented as an opaque string.
The existing update service reads and writes a whole person row; this API adds
no optimistic concurrency or durable replay guarantee.

Both operations require acting-administrator authorization, remain registered
when unavailable, and are non-retryable. Existing web editor actions disable
mutation retries and authentication replay. Apple, Android, and Jellyfin have
no corresponding administrator callers. Frozen v1 responses remain unchanged.

## Metadata translation jobs

These item routes use the `metadata_curation` permission, including delegated
curators, rather than requiring a server administrator. The item in the URL
is checked by the same permission middleware used by the frozen v1 routes.

| Endpoint | Result |
| --- | --- |
| `POST /api/v2/admin/items/{id}/metadata-translation` | `202` with the canonical translation job |
| `GET /api/v2/admin/items/{id}/metadata-translation/jobs` | `jobs`: the newest 50 jobs for this content ID |
| `POST /api/v2/admin/items/{id}/metadata-translation/jobs/{job_id}/cancel` | Empty `204` after a cancellation request |

Enqueue accepts `target_language`, optional `include_children`, and optional
`force`. Children default to included for item targets; season and episode
targets never include children. The requesting account is recorded by the
service. A matching active job can be reused; otherwise the service persists
and dispatches a job. `Location` identifies the item's recent-job listing.
Success does not mean translation has completed. The existing metadata AI
capability endpoint describes whether the translation provider is configured.

Job IDs use opaque strings and timestamps use UTC millisecond instants. The web
polls while jobs are pending or running. Cancellation checks that the job belongs
to the authorized content ID before invoking the service; a mismatch returns
`404`. Cancellation of running work signals its local runner, so a `204` does not
promise that a running job has already stopped. Existing terminal jobs are a no-op.

Enqueue and cancellation have no durable request replay receipt. The web enqueue
mutation disables both mutation retries and authentication replay. No existing
web cancel control, native administrator caller, or Jellyfin equivalent exists.

## Item metadata edits and refresh

`POST /api/v2/admin/items/{id}/refresh-metadata` accepts `mode: "quick"` (the
default) or `mode: "complete"`. The existing resolver chooses the refresh scope,
then a job is persisted with the requesting account ID. Success is `202` with
the typed administrator job, a job `Location`, and `Retry-After: 5`. Delegated
curators receive the permitted job projection and can poll their own item jobs.
The web waits on that job; it does not equate submission with completion.

`PATCH /api/v2/admin/items/{id}/metadata` retains the existing partial metadata
fields, including descriptions, dates, ratings, provider IDs, numbering and
locked fields. Omitted/null fields preserve values; explicit empty strings,
zeroes, and empty arrays remain edits. An air timezone must be empty or a valid
IANA timezone. The service tries item, season, then episode ownership and returns
canonical catalog detail after the update and existing invalidation events.
A follow-up detail-read failure can occur after persistence; it does not roll
back the metadata change.

Both operations preserve item-scoped `metadata_curation` authorization and are
non-retryable. Web actions disable both mutation retries and authentication
replay. No additional optimistic concurrency or request replay receipt is
introduced. Existing native viewers and Jellyfin reads remain separate from
these curation actions.

## Episode and movie marker analysis

`POST /api/v2/admin/items/{id}/refresh-markers`,
`POST /api/v2/admin/items/{id}/redetect-markers`, and
`POST /api/v2/admin/items/{id}/redetect-intro` require acting-administrator
authorization and run the existing local analyzer, which finds an episode's
intros and end credits and a movie's end credits. Movie credits are best
effort, and movies never get local intros.

- `refresh-markers` refreshes an episode or a movie from its configured
  sources. In `both` mode, local analysis then fills an episode's missing
  intro or credits, or a movie's missing credits, for the kinds the detection
  settings leave on, and is skipped when none apply. In `local` mode it runs
  local analysis of every kind that is on, and returns the same `409` as
  `redetect-markers` when none are.
- `redetect-markers` backs the web **Re-detect Markers** action for episodes
  and **Re-detect Credits** for movies. Its optional JSON body
  `{"kind": "intro" | "credits" | "all"}` selects what local detection runs
  again; an absent body or kind means `all`. For an episode, `intro` and
  `credits` run that kind alone and `all` runs both. For a movie, `credits`
  and `all` run the credits analysis. `intro` takes episodes only, so it
  answers a movie like any other item that is not an episode. Any other kind
  returns a `422` validation problem at `body.kind`. The requested kinds are
  narrowed to those `markers.detect_intros` and `markers.detect_credits` leave
  on; when none remain, it returns a `409` conflict whose detail names the
  kinds that are off, such as `Credits detection is turned off in marker
  settings`. A movie counts as a credits request.
- `redetect-intro` ports the v1 route and, like the v1 `refresh-markers` and
  `redetect-intro` routes, finds episode intros only, whatever the detection
  settings say.

For local analysis the item must exist, have media files, and have at least
one in a library with marker detection enabled: a series or mixed library for
an episode, a movie or mixed library for a movie. An item of the wrong type
returns a `422` validation problem (`400` on v1): anything but an episode for
`redetect-intro` and `redetect-markers` with `intro`, and anything but an
episode or a movie otherwise. Marker settings must allow local analysis; off
and online-only modes return `409`. Unconfigured dependencies return `503`.

`GET /api/v2/admin/markers/capabilities` tells a client which of these it can
use. `movie_credits: true` means the server accepts movie IDs in
`refresh-markers` and in `redetect-markers` with `credits` or `all`, and looks
for their credits locally. `redetect_markers: true` means the server offers
`redetect-markers`. `detection_kind_settings: true` means local detection
honors `markers.detect_intros` and `markers.detect_credits`. Servers without a
field lack that support: without `movie_credits` they analyze episodes only
and reject a movie, without `redetect_markers` the operation does not exist,
and without `detection_kind_settings` they store the two settings but detect
both kinds anyway, so clients should not offer them. The document describes the
build; marker settings and library switches still decide whether an item is
analyzed.

All three return `202` with `status: "queued"` or `status: "already_running"`.
These statuses acknowledge process-local background work. There is no persisted
job, job Location, cluster-wide exclusion, or restart recovery promise. Active
work is coalesced by item ID within the process, and one analysis of an item
runs at a time. While local analysis of an item runs, a request for kinds that
analysis, together with any work queued behind it, already covers reports
`already_running`. A request for further kinds, such as `credits` while an
intro-only analysis runs, reports `queued` and runs just those kinds once the
running analysis finishes. While an online `refresh-markers` runs, every other
request for the item reports `already_running`, and a `refresh-markers` that
would run online reports `already_running` while local analysis runs. The
frozen v1 routes also report `already_running` while any analysis of the item
runs and never queue. Successful analysis retains the existing marker-update
notifications.

The operations are non-retryable. The web re-detection actions disable mutation
retries and authentication replay. No native administrator caller or matching
Jellyfin action exists; playback marker reads remain separate.

## Seek preview administration

These operations require acting-administrator authorization: an administrator
account and, when `X-Profile-Id` is present, its primary household profile.
Discover the feature through `GET /api/v2/libraries/capabilities`
(`getLibraryCapabilities`), whose `trickplay: true` covers the library setting
and these administration operations. Library `trickplay_supported` separately
reports whether public asset storage is configured.

| Endpoint | Result |
| --- | --- |
| `GET /api/v2/admin/items/{id}/trickplay` | `200` with `files`, ordered by file ID |
| `POST /api/v2/admin/items/{id}/trickplay/regenerate` | `202` with `requeued`, the number of files queued ahead of the backlog |
| `GET /api/v2/admin/trickplay/libraries` | `200` with `items`, ordered by library ID |

The item ID can name a movie, episode, or series; a series includes all its
episode files. Each represented episode of a multi-episode file resolves that
file, including when it is missing. File and library IDs are opaque strings.
The arrays are complete lists, never null, without pagination.

Each file reports `state`, `servable`, and `failures`, with optional
`last_error`, `generated_at`, `thumbnail_count`, `thumbnail_width`, `interval_ms`,
and `sheet_bytes`. `state` is `off` for a disabled library, a library with an
unsupported type, or one with previews turned off; `unusable` for an ineligible
file or a permanent sampling failure; otherwise it reflects `pending`,
`running`, or `ready`. An eligible file with
no queue row yet reports `pending`. `servable` independently reports whether
the current store has a published revision matching the file: previous sheets
can remain available after a failed or unusable regeneration. Turning previews
off or disabling the library suppresses availability immediately. Published
timestamps are canonical UTC instants; widths are pixels, intervals are
milliseconds, and storage sizes are bytes.

Regeneration queues eligible video files in enabled, opted-in libraries and
clears their failure backoff. Missing, unprobed, zero-duration, and audio-only
files are skipped. Running files finish their current attempt. A successful
response may report `requeued: 0` when no eligible file needs queuing; `202`
acknowledges persisted queue changes rather than completed extraction. Previous
published sheets keep serving until replacements publish. There is no
administrator job resource or replay receipt. Repeating the request after work
finishes queues another generation, so it is non-retryable; clients must disable
automatic mutation retries and authentication replay.

The library listing includes enabled video libraries with previews turned on.
Each entry contains `library_id`, `name`, `pending`, `running`, `ready`, `unusable`,
and `sheet_bytes`. Pending counts include failure backoff and eligible files
not yet reconciled into the queue. A tracked file that becomes missing,
unprobed, zero-duration, or audio-only counts as `unusable` regardless of its
persisted queue state. Ineligible files that have never entered the queue do
not contribute to these counts. Status reads do not alter queue rows. Storage
sums the retained published sheets; generation counts do not replace each
file's `servable` value.

The item operations return `404` `not_found` when no associated media file
exists. Regeneration returns `409` `capability_disabled` when none belongs to an
enabled, opted-in video library. All three return `503` `dependency_unavailable`
when the service is unconfigured, and `500` `internal_error` on an unexpected service
failure, using the common Problem response. Ordinary profiles receive `403`
`permission_denied`; authentication failures use the common `401` problems.
Web administration uses these operations. Apple, Android, and Jellyfin have
no corresponding administration callers; their playback preview reads are
separate.

## Marker edit history

Acting administrators can read recent edits through `GET /api/v2/admin/markers/history`,
`GET /api/v2/admin/markers/files/{fileId}/history`, and
`GET /api/v2/admin/markers/items/{id}/history`. Each returns a `history` array,
newest first, with `limit` from 1 to 100 (default 25). These are bounded recent
results, without a continuation cursor. An unconfigured audit reader returns
an empty array. File selection retains catalog access authorization; item
selection includes every file version under the existing administrator scope.

Audit, file, account and API-key IDs are opaque strings. Timestamps are canonical
UTC instants. Before/after snapshots use `start_seconds` and `end_seconds`; an
absent snapshot means no marker existed on that side of the edit. Existing admin
attribution and request audit fields remain available. The web history views
preserve string IDs and adapt absent snapshots for display. Native clients and
Jellyfin have no corresponding administration callers.

## Marker providers

`GET /api/v2/admin/markers/providers` lists registered provider configurations,
ordered by fetch priority then provider ID. Plugin installation IDs are opaque
strings. `PUT /api/v2/admin/markers/providers/{provider}` retains partial-update
semantics: omitted/null members preserve current values; explicit false and zero
remain edits. Confidence must be between 0 and 1. Persistence and the existing
configuration event run synchronously; there is no revision precondition or
whole-operation replay receipt.

`POST /api/v2/admin/markers/providers/{provider}/validate` asks the provider for
contribution statistics. It returns `200` with `valid: true` and typed statistics,
or `valid: false` and a generic error without the provider's raw error text.
Providers that cannot submit return a validation Problem. All three operations
require acting-administrator access. Web mutations disable both retry layers.
No native client or Jellyfin administration caller requires migration.

## Marker contributions

`POST /api/v2/admin/files/{fileId}/contribute` requires acting-administrator and
file access. Its optional body selects a provider and marker segments; an absent
body uses the existing contributor defaults. The request waits for contribution
processing and returns `200` with per-provider outcomes, including skipped or
failed outcomes. Provider-specific claims do not make the whole request
replay-safe across providers; the operation is non-retryable.

A claim covers the provider item (show, season and episode, or movie), not the
submitted times: providers accept one submission per account for each item and
segment and cannot amend it, so a later payload for a claimed item is skipped.
A provider refusal of the item itself, such as a season the provider's catalog
does not list, is recorded with status `invalid` and holds the claim for 30 days
before one more attempt. Retryable errors release the claim.

`GET /api/v2/admin/files/{fileId}/contributions` applies the same access checks
and returns `items` plus `page`, with default limit 50 and maximum 200. Database
keyset paging sorts by descending `updated_at`, then contribution ID. Cursors
bind the operation, account/profile/access scope, file, limit and sort. This is
live history: a row updated during traversal may move ahead of the current page;
there is no snapshot or lossless synchronization guarantee. File IDs are strings,
instants are canonical UTC, and the explicitly named millisecond fields retain
their existing units. Frozen v1 keeps its original full-list transport. No
first-party web/native or Jellyfin administration callers were found.

## File grouping repair

`GET /api/v2/admin/items/{id}/files` returns administrator-visible files through
SQL keyset pages ordered by ID (default 50, maximum 200). Cursors bind the item,
account/profile, limit and sort. IDs are opaque strings. The web split dialog
collects complete pages under one captured authority and rejects repeated cursors
or an authority change before publishing the file list.

`POST /api/v2/admin/items/{id}/split` retains the existing target choices,
`history_mode` (`evidence`, `keep`, `move_all`), identity overrides and dry-run
behavior. `file_ids` are strings, with at most 10,000 selected files. Omitted/null
`persist_override` retains the existing default of true. A dry run performs the
transactional move and reattribution calculations, then rolls back. A committed
split persists those changes before existing follow-up identification/refresh.
The result reports counts and capped ambiguous-history samples using string
account IDs and canonical watched-at instants.

`POST /api/v2/admin/items/{id}/merge` retains the `into` target and waits for the
existing merge and best-effort target refresh. Neither mutation supplies a
whole-request replay receipt; both are non-retryable. The split web action
disables mutation retries and authentication replay, including dry runs. All
three operations require acting-administrator access. No native or Jellyfin
administration caller was found; frozen v1 adapters retain their original wire
shapes and behavior.

### Match repair

`POST /api/v2/admin/items/{id}/match/search` and `/match/apply` require the
item-scoped `metadata_curation` permission, including the existing item access
check. Library IDs are strings. Search preserves the existing provider-ID
normalization, title/year fallback, library selection and provider ranking.
Its optional `limit` defaults to 100 and accepts 1–500. The response contains
`candidates` (never null) and `truncated`; refine the search when truncated.
This bounds the response, not the provider fetch or normalization work, and
provides no snapshot or continuation guarantee. The web requests 500 candidates
and displays the refinement notice when needed.

Apply requires nonempty provider IDs and runs the synchronous metadata
pipeline. It returns `content_id` and `updated`; it is not a queued job. The
returned `content_id` differs from the path ID when the item moved: a `local-`
item takes its provider-anchored ID, and an item whose match the choice
corrects moves to the ID its new identity derives (see
[deterministic content IDs](architecture/deterministic-content-id.md#re-anchor-on-a-corrected-match)).
Callers holding the old ID should switch to the returned one. Neither web mutation automatically retries or refreshes
and replays authentication. Frozen v1 responses and parsing order remain
unchanged. These administration flows have web consumers; Apple, Android and
Jellyfin have no corresponding caller in the migration inventory.

### Unmatched files

`GET /api/v2/admin/unmatched` lists actual files with neither a content match nor
an extra association. Acting administrators receive `items` and `page`; file and
library IDs are strings. `limit` defaults to 50 and accepts 1–200. SQL applies the
ID-ascending keyset and limit before scanning rows. Signed cursors bind the
operation, authority and page size. This is a live collection: concurrently
matched files may disappear. Frozen v1 retains its offset pagination and raw
array. No current web, native or Jellyfin consumer needs migration for this route.

### Item image selection

`GET /api/v2/admin/items/{id}/images` requires acting-admin access and returns
`items`, `page`, `current`, and optional `provider_errors`. Pages default to 50
choices and accept `limit` 1–200. Choice metadata determines a stable order;
continuation binds the content ID, authority, limit and complete choice digest.
Expiring display URLs are excluded from that digest. A provider choice change
returns `invalid_cursor` and requires reloading. Each page fetches and resolves
the provider list again; pagination bounds response size, not provider work or
memory, and stores no server-side snapshot. Provider failures expose generic
messages. The web drains all pages under one captured authority and refuses
repeated or invalid continuation rather than publishing a partial list.

For a movie or series the list also offers its local sidecar artwork (the
`poster.jpg`, `fanart.jpg` and similar files a refresh would use), found by
Silo's own sidecar discovery whether or not the library's metadata chain uses
the NFO provider. A `file://` URL from any other provider is not offered. These
choices have `provider_id` `local`, a `file://` `original_url`, and a small
WebP preview as a `data:` URI in `url`. A file that cannot be read under the
library-root, symlink and size checks is left out, and `provider_errors`
reports the failure under `local`. Frozen v1 lists provider choices only. See
[Local NFO metadata](architecture/local-nfo-metadata.md#admin-image-picker).

`POST /api/v2/admin/items/{id}/images/apply` accepts `original_url`, `type`, and
optional `provider_id`. An HTTP(S) `original_url` may name a public or
local-network address; link-local, cloud metadata and other blocked addresses
are refused (see
[Outbound address guard](architecture/outbound-address-guard.md#artwork-downloads)). A `file://`
`original_url` is accepted for a movie or series only when the item's sidecar
discovery offers that file for that image type; anything else returns a `422`
validation problem. A failure to read or cache an offered file returns `500`.
Frozen v1 does not accept local files.
It preserves target validation before remote work,
episode-to-still coercion, parent/season/episode cache identity, immutable upload,
transactional catalog publication and orphan-GC scheduling after publication
failure. Success returns the stored path, thumbhash and available revision/display
URL. This is synchronous and may fail after upload; it is not a persisted job
or replay-safe operation. Both web retry layers are disabled. Frozen v1 retains
its response shapes and error codes. No native or Jellyfin caller is present in
the migration inventory.
