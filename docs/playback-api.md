# Playback API (v2)

`/api/v2/playback` is the native playback contract. Protocol version 3 decides
the route ([playback protocol v3](architecture/playback-protocol-v3.md)); this
document covers the v2 envelope around it: the installation check, sequenced
progress and stop, delivery, and revocation. Every operation requires an
authenticated account and an active profile. Validation failures are `422`; an
unavailable store is `503`.

## Operations

| Operation | Method and path | Success |
| --- | --- | --- |
| `getPlaybackCapabilities` | GET `/api/v2/playback/capabilities` | 200 capability state |
| `startPlayback` | POST `/api/v2/playback/start` | 201 v3 decision |
| `updatePlaybackProgress` | POST `/api/v2/playback/{session_id}/progress` | 200 mutation receipt |
| `stopPlayback` | DELETE `/api/v2/playback/{session_id}` | 200 mutation receipt |
| `replanPlayback` | POST `/api/v2/playback/{session_id}/replan` | 200 v3 decision |
| `getPlaybackInventory` | GET `/api/v2/playback/{session_id}/inventory` | 200 live audio and subtitle inventory |
| `reportPlaybackRouteEvent` | POST `/api/v2/playback/route-events` | 202 `{event_id, outcome: "accepted"}` |
| `getPlaybackBandwidthProbe` | GET `/api/v2/playback/bandwidth-probe` | 200 random bytes, or 307 to a proxy |
| `reportPlaybackBandwidthProbe` | POST `/api/v2/playback/bandwidth-probe/results` | 204 |
| `getPlaybackConnection` | GET `/api/v2/playback/connection` | 200 connection state |
| `resetPlaybackLearnedLimit` | DELETE `/api/v2/playback/connection/learned-limit` | 204 |

## Live inventory

`getPlaybackInventory` returns the live audio and subtitle inventory of an
active session and supports `ETag` / `If-None-Match` (`304` when unchanged). The
revision the ETag is derived from includes the effective version identity
(`effective_media_file_id`, `effective_virtual_uri`,
`virtual_source_revision`), so a rotation to a sibling release changes the ETag
even when the new release's declared inventory is identical. It also names the
version the transport is committed to:
`effective_media_file_id`, `effective_virtual_uri`, and
`virtual_source_revision`. These are read from the live session, so they move
when a serve-layer rotation rebinds the session to a sibling release — unlike
the plan's `effective_media_file_id`, which keeps the identity it was built
with. A client polls this endpoint (or consumes the realtime `source_committed`
event) to follow the streamed version. The response's inventory is the committed
release's own; a rotated session whose new release is not yet probed reports
`inventory_status: declared` and that release's declared (possibly empty)
tracks, never the previous release's. When no catalog row names the bound
candidate at all, the response falls back to the inventory the running plan
carried for that binding instead of an empty placeholder, so the player's
overlay menu still lists what the plan committed to; the catalog is not
rewritten by that read. That overlay is served only while the session's
evidence URI still names the bound candidate. Evidence captured before a
binding rotation belongs to the previous release, so the response keeps the
empty fail-closed placeholder rather than presenting the old tracks under the
new identity.

The downloaded/AI subtitle rows are an extension of the base inventory. If that
extension cannot be read, the endpoint still answers `200` with the embedded and
external tracks and reports `inventory_status: pending`, rather than failing the
whole menu with `503`. Here `pending` means the base inventory is complete and
only the extension is missing; it does not mean the base menu is unusable.
Clients keep the current menu, honor the response `ETag` with `If-None-Match`,
and retry with backoff: about 2 seconds for the first poll, backing off to at
most one request every 20 seconds, and stopping on a terminal status or the
realtime `inventory_updated` event. Do not busy-poll: while the subtitle store
is unavailable the extension stays pending, and a tight loop only adds load. A
later successful read includes the downloaded rows and reports a new
`inventory_revision`, so a client holding the degraded menu can replace it
instead of staying on `304 Not Modified`.

### Deferred track inventory

A cold start normally resolves a virtual source from provider-declared metadata
and upgrades it with a full ffprobe enumeration. On `/api/v2` a client that
advertises `deferred_track_inventory_v1` may instead receive a plan whose
first-byte URL is already playable while the enumeration runs after the
transport commit. The plan carries `tracks_pending: true` and
`inventory_url`; the menu is provisional until the follow-up lands. The frozen
`/api/v1` surface and a v2 client that did not advertise the feature never see
`tracks_pending` and keep the pre-existing behavior, so the deferred lifecycle
is strictly opt-in.

`inventory_url` is not gated by the negotiation: it is present on every fresh
plan, negotiated or not. It names the same live-inventory endpoint
`getPlaybackInventory` serves, and v2 clients have always been able to poll it.
What the negotiation adds is the promise that the menu may be provisional, which
is exactly what `tracks_pending` signals. A start that does not negotiate the
lifecycle still probes its source (inline when the start is synchronous, in the
background otherwise) and does not gate the menu on that probe; if it polls
`inventory_url` anyway, it simply observes `declared` until the probe lands.

The lifecycle is start-only. `replanPlayback` never defers: a replan either
replays the attempt's plan — including its `tracks_pending` marker — or runs its
own synchronous resolve, and it always carries `inventory_url`. A client must
therefore read `tracks_pending` from a replan response too, not assume the
lifecycle ended at start.

The session's inventory status moves `declared` → `pending` → `verified` or
`failed`. `pending` means the enumeration is outstanding; the client keeps the
provisional menu. `verified` means the enumerated tracks are committed and the
follow-up `inventory_updated` push (or a poll of `inventory_url`) replaces the
menu. `failed` is terminal: the enumeration could not be completed — a probe
error, an unreadable evidence write, or a saturated worker pool that could not
admit the probe — and the client must leave the loading state and keep the
provisional menu it already holds. A failed upgrade never changes the stream or
the executable audio selection; a viewer who picked an audio track keeps it.
The deferred probe's pending state is bounded: the server tracks when each
deferred probe was armed and terminally fails one that has not reached a
terminal outcome within the convergence bound — the probe's own budget plus a
fixed slack for the evidence write and the publish handoff. That bound covers
the probe lifecycle, not every `pending` the endpoint can report. The probe
itself therefore always reaches `verified` or `failed`, even when a provider
ignores cancellation or the probe pool stays saturated. The downloaded-extension
path above is separate and has no server-side deadline: the extension is
retried, not probed, so its reported `pending` is bounded only by the client's
poll and backoff (about 2 seconds, backing off to at most one request every 20
seconds). While the extension stays unreadable a terminal probe outcome remains
owed rather than delivered, so the client keeps seeing `pending`; the outcome
surfaces in the original session as soon as an extension read succeeds.
When the durable evidence write commits a verified inventory whose order differs
from the declared one, the server replays the attempt's persisted audio
selection intent against it (the same default-audio reconciliation the
non-deferred evidence path runs) and asks the client to replan through the
existing `plan_invalidated` handshake, so the executable recipe keeps the
preferred language. An explicit viewer selection is verified present and never
overridden. A selection that is genuinely absent from the committed inventory is
never substituted: the verifier records an `AudioReconcileRefused` ledger entry
and warns, and a virtual remux start answers a retryable
`409 audio_selection_invalid` for the client to replan from, with no route event
ending the session. The refusal entry and the transport's invalid-selection flag
are independent: the overlay remap sets that flag on its own, and only while the
committed index is still in range.
`tracks_pending` is a menu hint and is excluded from plan identity, so an
idempotent start retry replays the same plan with the same marker.

Apple and Android clients that adopt the feature must: read `tracks_pending`
from the start and replan responses and render the track menu as loading while
it is true; consume the existing `inventory_updated` realtime event
(`inventory_updated_event_v1`) or poll `inventory_url` (honoring its `ETag` /
`If-None-Match`) until the status is terminal; adopt the enumerated menu on
`verified`; on `failed`, stop showing the loading state and keep the current
menu and audio selection without issuing a replan; and drop the marker on any
plan replacement. Until they do, they must not send
`deferred_track_inventory_v1`.

A menu pick made while the inventory is still deferred may carry only the
ordinal it displayed, because the plan has no list to reconcile against. That is
safe: a `track_change` sends the bare positive ordinal, and the server resolves
it against the effective file's own inventory (external, then embedded, then
downloaded subtitles; the effective file's audio tracks). On a live session, an
ordinal that inventory does not carry — the deferred probe has not landed, or
the selection names a track the file lacks — degrades to subtitles-off rather
than selecting track 0, and an out-of-range audio index degrades to the file's
default track; neither is a `400`. Once the plan publishes an inventory,
reconcile by `track_id` first rather than by ordinal.

A deferred (or otherwise unestablished) inventory is now explicitly labeled on
the plan. When `subtitle.inventory` is empty for a reason other than a verified
"this file has no subtitles", the plan carries
`claims.subtitles.reason: "inventory_unavailable"`, and `tracks_pending: true`
when the deferred lifecycle applies. A client that negotiated
`subtitle_inventory_unavailable_v1` renders the menu as still discovering (not
as final-and-empty) and must not turn a catalog fallback row into a selectable
ordinal: the ordinal it shows belongs to another inventory. Prefer `track_id`,
and hold a pick until a verified inventory publishes.

A `track_change` whose subtitle this inventory cannot honor answers with the
plan still playable, `subtitle.mode: "off"`, and
`claims.subtitles.reason: "selection_unavailable"`. That is a terminal answer
for the pick: the client surfaces it, clears the optimistic row, and does not
replay the same `(file, subtitle identity)` until the inventory or the user's
choice changes. A client that negotiated `subtitle_selection_rejected_v1`
treats the reason as authoritative instead of reading subtitles-off as a
successful pick. An explicit subtitles-off request carries no reason.

## Capabilities

The response is `{installation_id, revision, state, allowed, protocol_versions,
features, deliveries}` with `Cache-Control: private, no-cache` and an `ETag`; clients may use `If-None-Match` and receive `304 Not Modified` when unchanged. `state` is `available`
and `allowed` is `true`; a server without playback wired answers
`not_configured` with `allowed: false`. `installation_id` is the persisted
server instance UUID that diagnostics also report. `protocol_versions` is
`[3]`. `features` is the v3 server feature set plus `sequenced_progress_v1`,
`fixed_media_file_v1`, `marker_segments_v1`, `trickplay_v1`,
`bandwidth_probe_v1`, `connection_speed_v1`, and `versions_check_v1`. With `connection_speed_v1` the
response adds `bandwidth_probe_url`, the media-path probe below, on the origin
the request arrived on, with `X-Forwarded-Proto` and `X-Forwarded-Host`
counted only from a trusted proxy. A reverse proxy that rewrites `Host` to a
bare IP address and names no single client host in `X-Forwarded-Host` as a
trusted proxy leaves only the address it dialed; then the URL is on
`server.public_url` when one is configured. When the origin
is the server's local-network origin (a local route), clients send the probe with the saved server's credentials and trust,
as they do for any request to that server. The response also adds the speed
test schedule:
`bandwidth_probe_automatic` (the `playback.bandwidth_probe_automatic` server
setting), `bandwidth_probe_max_age_seconds` (from
`playback.bandwidth_probe_max_age_days`) and, when the server has video
bitrates to go on, `bandwidth_probe_target_kbps`. The target is
`max(ceil(P × 5 / 4), 100)`, at most 1,000,000, where `P` is the lower of the
administrator bitrate cap for the caller's network, when one applies, and the
99.9th percentile of the video bitrates in the libraries the profile can play.
The [buffering-quality design](design/2026-10-17-buffering-quality.md#connection-speed-test)
defines how clients test with them. When the room service and
authenticated room socket are configured, it also includes
`watch_party_source_fallback_v1` and `watch_party_coordinator_v1`.
The coordinator capability covers shared room
membership, command-aware readiness, and ready/buffering/syncing member status.
`deliveries` lists `original_http`, `server_remux_progressive`,
`server_remux_hls` and, when transcoding is enabled, `server_transcode_hls`.
`revision` is a digest of the rest.

The `bandwidth_probe_v1` and `versions_check_v1` entries are additive
discovery: a client that ignores either keeps its previous behavior. No start,
replan, or other playback path requires calling the bandwidth probe or the
catalog versions check, and the capabilities response gains no required member
when it advertises them.

## Bandwidth probe

`GET /api/v2/playback/bandwidth-probe?size=N` (feature `bandwidth_probe_v1`)
sends `N` random bytes, 1 to 1,000,000,000, default 1,000,000, as
`application/octet-stream` with `Cache-Control: no-store`. It needs an
authenticated account but no profile. Each server runs at most 4 probes at
once. On each server an account may run two of them and draw up to
2,000,000,000 bytes per 10 minutes; bytes a client does not read because it
closed the connection early are not counted. Beyond any limit the probe gets `429 rate_limited` with
`Retry-After`. The bytes are random so a compressing
proxy cannot inflate the result. A client times the transfer from its first
byte, may close the connection once it has timed enough, and sends the result
as `bandwidth_estimate_kbps` on start. Replans never carry it. Without a measurement a
browser has only `navigator.connection.downlink`, which Chromium caps at
10 Mbps, too low for Auto quality ever to send a 4K source as-is. Its bytes
count as viewer transfer egress, not playback, and are exempt from stream caps,
like the Jellyfin-compatible `/Playback/BitrateTest`.

Without `media_path` the probe measures the path to the API server, and media
served from a remote node can take a different one. `media_path=true`, the form
`bandwidth_probe_url` advertises, measures the media path instead: the server
picks the egress that the direct-play routing policy would use for the
caller's network or provider path. When that is a proxy, the answer is `307` to
the proxy's `/stream/bandwidth-probe/{token}?size=N` with a two-minute token
valid only for probes; the proxy applies the same limits. When the API serves
direct play, it sends the bytes itself. When a proxy-only policy has no proxy
that serves probes, the answer is `503 dependency_unavailable`, never a
measurement of the API path.

The web app reads the connection endpoint as soon as a profile is active, when
a tab hidden for a minute or more is shown again, after the browser reports a
new connection type, and when it comes back online, so a playback start in the
first seconds already sends the reading. A start sends a reading the server
kept only after this page has read it, never one left from an earlier visit. A
tab back from a shorter switch sends nothing unless the hide stopped a test it
owed, which it then resumes without reading again. A few seconds later it runs
the quick test only when no current reading proves the link (any reading does
when capabilities carry no target). It never runs one while the tab is hidden,
on Data Saver or cellular, on a phone or tablet whose browser does not report
the link type, while any media plays (theme music included) or a start loads,
or within a minute of a playback start; playback starting cancels a running
test. A `429` from any test holds automatic tests for its `Retry-After` (30
seconds when missing), across profile switches. **Test speed** runs the
accurate test, only while nothing plays or loads; it replaces a running
automatic test, and a second press joins the running one. Tabs share the
reading, and a Web Lock lets only one of them probe at a time. A tab holds a
shared lock while its media plays or a start loads, and releases it when the
player goes away; no tab probes while any tab holds it. A playback start is
also broadcast to the other tabs, so a probe already running in one of them
stops and discards its measurement. On a metered link (Data Saver or cellular)
it sends no cached measurement. Signing out discards the kept readings.

Every mutation body carries the `installation_id` the client read from
capabilities. A different value is `409 installation_changed`: refresh
capabilities and start a new attempt. There is no admission step and no
per-account enrollment. `getPlaybackInventory` is the exception: it is a
session-scoped read whose ownership is enforced against the stored live
session (the attempt row is required but not an ownership check), so a stale
`installation_id` (capabilities cached across a restart) still resolves the
pending track inventory instead of 409-looping.

## Indexer releases

V2 watch detail carries an additive `indexer_releases` array alongside
`versions`: releases that exist on the configured indexers but are not
downloaded on the provider yet. The array is always present and empty when
there is nothing to request. Each entry is
`{release_id, title, resolution?, codec_video?, codec_audio?, hdr?,
size_bytes?, indexer?, published_at?, format_score?, protocol?, download_state}`.

`release_id` is an opaque server row id; it is never the release GUID or the
download URL. `download_state` is `not_downloaded`, `queued`, or `failed`.
`published_at` is an RFC 3339 instant. The array is empty for local content,
for servers with no indexer configured, and for `/api/v1` and jellycompat,
which do not carry it.

`POST /api/v2/media/{media_id}/virtual-releases/{release_id}:request` requests
one release on the provider. The server resolves the stored row scoped to the
item's content id (and episode id where applicable) and media folder, re-checks
the caller's access through the same path the watch detail uses, and hands the
server-stored `download_url` to the provider. The request accepts no URL and
never echoes one; the download URL is never serialized or logged. The response
is `200 {release_id, state, message?}`, where `state` is `queued` or `failed`.
It is idempotent (`domain_identity`): an already-queued release returns
`queued` without a second enqueue, and a provider refusal records `failed` so
the row stays retryable. An unknown or foreign release id is `404`; a
malformed id is `422`; an unconfigured provider is `503`.

`GET /api/v2/capabilities/virtual-library` is the feature-detection document:
`{revision, state, indexer_search, indexer_request}`. `state` is `available`
when either path is wired and `not_configured` otherwise. `indexer_search`
reports whether Prowlarr search is configured and `indexer_request` whether
provider enqueue is configured; clients that cannot request releases must not
offer the action. `discover_before_playback` reports whether opt-in pre-play
version discovery is enabled and wired; it also makes `state` available.

### Pre-play Discovery

`virtual_library.discover_before_playback` is a server-wide streaming setting,
defaulting to `false`. The capability and discovery operation read it on each
request, so saving the setting does not require a restart.

When enabled, the web detail page calls
`POST /api/v2/media/{media_id}/virtual-candidates:discover` for the selected
virtual movie or episode, without waiting for Play. The operation enforces the
same account/profile access filter as watch detail. It reuses the provider
candidate cache, coalesces concurrent discovery of the same source, persists
non-empty listings, and answers `204 No Content`. A provider listing that
succeeds with no candidates is a valid empty discovery: it answers `204`,
persists nothing, and leaves stored candidates intact. A disabled or unwired
service answers `503`; a whole-series or non-virtual target answers `422`.
Provider unavailability and timeouts answer `503` and leave existing candidates
intact. The explicit Refresh List action stays available as the retry path.

The operation is a listing surface: it prepares nothing. Only a detail-page
read starts a pre-play preparation, so a client that calls the operation for
its own reasons cannot spend provider traffic on background probing.

Discovery does not request downloads, search indexers, or open media URLs.
After success, clients reload item and watch detail to display the persisted
versions. Play remains available during discovery, and playback-time resolution
remains the fallback. The web client initiates discovery only for detail views,
not list cards or whole seasons/series, and reuses a recent discovery for 30
seconds. Native clients and Jellyfin clients keep their existing playback-time
behavior when the setting is off.

When enabled, `GET /api/v2/watch/{id}` also discovers versions after checking
access for a virtual movie or individual episode. It uses the same cached
discovery service and re-reads watch detail before answering, so existing
native clients receive the persisted versions without calling the
discovery operation themselves. Discovery has a 5-second waiting budget. A
provider listing that takes longer than that is abandoned and the original
detail is returned, so a cold provider never blocks the read beyond the budget;
shared provider work may finish after that wait and be visible on a later
request.

When enabled, a leaf catalog detail read — `GET /api/v2/catalog/items/{id}`
resolving to a virtual movie or individual episode — starts the same discovery
detached and answers immediately from the persisted version list: browsing is
the hot path and native clients such as Android read item detail alone before
play. Shared provider work keeps running after the response under its own
budget, so a listing that finishes later is visible on a later read. Discovery
failure or a timeout changes nothing in the answer. Eligibility reads the
stored source rows and never the response's redacted file paths, so viewers
without file-path visibility reach the same discovery.

The detached catalog budget is tuned with the admin Streaming setting
`virtual_library.catalog_discovery_budget_ms` (500-20000, default 20000). It
is read on each detail request, so saving it does not require a restart.
Measured provider scrapes were observed at 17–25 seconds, which is why the
budget is sized to admit that band; the watch path keeps a shorter 5-second
wait because its caller is opening the player. Both paths continue the provider
work detached after the response, so a cold scrape is not lost when the
response returns first.

Leaf answers carry an additive `has_virtual_source` boolean. It reports
whether the item holds persisted virtual candidate groups, read from the same
stored rows discovery uses, and it exists independently of whether discovery
is enabled or its budget expired — a viewer whose file paths are redacted
still learns whether the item's source rows live on the provider. The field
does not promise additional playable versions beyond what the versions
fields list, and `true` is its only rendered value: local items, non-leaf
items (series, season, lists), and failed lookups leave the field out. It
appears on `GET /api/v2/catalog/items/{id}`, `GET /api/v2/watch/{id}`, and
the admin metadata update response, keeping v1 answers byte-identical.

A detail discovery also hands the listing it just persisted to the playback
handler before its waiters return. The start that follows — the concrete
candidate row a detail page lists, or the provider-neutral placeholder — reads
its candidate metadata from that entry instead of listing the provider again,
so a first play after a detail read costs the one listing the read already paid
for rather than a second one, and a repeat play within the entry's lifetime
lists nothing. A forced relist, a decode-rotation exclusion, and a row the
catalog indicted each still list, because those need the provider's current
answer rather than the one the detail read saw.

After a detail-page read persists a listing, the server schedules one detached
pre-play probe chain for the opened item: it re-reads the item's selected
version when the detail read supplied `file_id`, otherwise its default version.
The chosen row is a persisted candidate; when a fresh listing no longer names a
selected profile variant, the neutral source row may probe that row's own stored
bytes after the target's authorization checks. The chain resolves the row's
stored provider URL (or fresh resolve) before probing; a durable `virtual://`
catalog URI is never handed to ffprobe. It probes the chosen candidate through
the ordinary background evidence path (queued, coalescing write). A neutral
group row is never probed speculatively — it may resolve to a sibling
candidate, so the play path's own probe handles it. Chains are bounded (two
concurrent, one per item, a whole-chain deadline, and the shared probe
gate), never block any API response, and never wait for playback: a chain may
record evidence before a play attempt resolves the same row, in which case that
attempt reuses it, and a play attempt that resolves first joins the chain's
in-flight probe instead of cancelling it. One ffprobe per candidate results:
the shared probe cache single-flights the enumeration, so the chain's work is
never discarded and then repeated. Foreground probing may still be required
when the chain has not finished, or when it prepared a version the play attempt
does not resolve.
These bounds are per process: a deployment with several replicas prepares
independently on each one. The queued evidence write follows the existing
persistence rules, including their stale-write protections; probing
failures arm the same failure damper the playback path uses, an
already-stamped row is skipped, and every unexpected chain failure —
including a panic — is contained to the chain.

A successful detail discovery is memoized for 30 seconds per viewer, item,
selected file, and library. The player's inventory poll re-reads watch detail
every few seconds while a probe is pending; those reads deliberately bypass the
client cache, and without the memo each one would re-list and re-persist every
candidate. A different selected file starts its own discovery immediately, and
the explicit discovery operation and refresh action bypass the memo.

The evidence write also tolerates one catalog-revision race that a re-list can
cause: if the row's exact path, owner, and probe stamp are unchanged and only
`updated_at` advanced, the write re-reads the row once and retries against the
new revision. A changed path, owner, or probe stamp is not re-based, so a
concurrent probe or a different candidate still wins.

The explicit discovery operation, the refresh action, search, browse, and
series, season, and episode listings never start a preparation. A listing-only
run and a detail run are separate work and do not coalesce, so a listing can
never decide whether a detail read gets its preparation.

`discover_before_playback` promises that the provider listing is fetched and
persisted. It does not promise that the preparation has finished.

Local-file details, version-only routes, browse results, and series, season,
and episode lists — including `GET /api/v2/catalog/items/{id}/versions`,
`/episodes`, and the series season endpoints — still never initiate discovery.
A view served entirely from the client's cache cannot. This does not add a
Jellyfin-protocol trigger.

Server logs identify these paths with `pre-play version discovery started`,
`pre-play version discovery completed`, or `pre-play version discovery unavailable`
and `trigger=watch_detail` (watch detail) or `trigger=catalog_detail`
(catalog leaf detail). A successful discovery
followed by a failed detail re-read logs `pre-play version discovery detail reload failed`.

### Refresh List

`POST /api/v2/media/{media_id}/virtual-candidates:refresh` is asynchronous. It
answers `202 Accepted` with `Location: /api/v2/admin/jobs/{id}` and
`Retry-After: 5`; clients poll `GET /api/v2/admin/jobs/{id}` until the job is
terminal, then re-read the watch detail. One active job exists per title (a
partial unique index on `(job_type, request_payload->>'content_id')`); a
concurrent refresh returns the existing job. The job is owned by the requesting
user, and a non-administrator can read their own job status (and only their
own). The pipeline runs, in order:

1. re-list and persist the provider candidates; a failure here fails the job;
2. an on-demand indexer search under its own short budget; a failure, timeout,
   or oversized answer is warned and the job continues with the provider-only
   result;
3. filter to usenet releases the provider does not already list (the dedup set
   includes badge-confirmed and cached streams), cap the batch, and persist
   them;
4. publish `catalog.item.changed` with `{content_id, change: "versions_updated"}`
   on the catalog channel so every client invalidates the version list.

The refresh is list-only: it never opens a candidate's media URL (no ffprobe,
no seek-anchor probe, no range fetch). Opening a provider media URL can
activate the provider, so verified audio/subtitle track inventory arrives with
an explicit Play of one candidate instead of during the refresh.

The completed job result carries the retained `indexer_releases` list in the
same wire shape the watch detail uses. The synchronous behavior this endpoint
previously had is gone; there is no second endpoint.

## Watch library context

On `GET /api/v2/watch/{id}`, `library_id` accepts an episode when either the
episode or its series belongs to that library. The series must still pass the
viewer's access checks. The response includes every file the viewer may access,
even if the selected library has none of that episode's files;
`catalog.scope_versions_to_library` does not narrow watch responses.

## Watch artwork

`GET /api/v2/watch/{id}` includes optional `poster_url`, `poster_thumbhash`,
`backdrop_url`, and `backdrop_thumbhash` for system media controls. Movies
use their own artwork, episodes use their still as the poster and their
series' backdrop, and local extras use their own still as the poster when they
have one and otherwise inherit their parent's artwork. URLs use
the same resolver and `image_size` query as catalog item detail. Missing
artwork is omitted; clients can retain their catalog-detail fallback.

## Marker ranges

V2 watch detail includes `marker_segments` on each file version, including
versions nested in playback variants. Each entry is
`{kind, start_seconds, end_seconds}`, with kind `intro`, `credits`, `recap`, or
`preview`. The collection is ordered by source time and is empty when the file
has no markers. A kind may occur more than once: skip only the active range,
without spanning intervening content. Existing singular `intro`, `credits`,
`recap`, and `preview` fields remain for older clients.

Marker reads and watch detail can populate the selected file's markers after
authorization. A provider error leaves the available markers readable.
See [Marker API](markers-api.md) for reads, manual edits, and provenance.

## Seek previews

The `trickplay_v1` capability covers seek-preview manifests at
`GET /api/v2/watch/{id}/trickplay?file_id=`. Clients fetch the sheet URLs the
manifest returns and refresh the manifest after `expires_at`. Local storage
and S3 with a separate public or token-authenticated delivery endpoint return
signed `/api/v2/artwork/...` URLs; the server reads the storage API to avoid
external delivery lag. Standard S3 delivery returns direct presigned URLs.
See [trickplay](architecture/trickplay.md#serving) for access rules, sheet
geometry, and revision retention. Like `getWatchState`, the manifest read
accepts a request without `X-Profile-Id` only while no profile on the account
is PIN-protected or access-restricted; otherwise it answers `422
validation_failed` at `header.x-profile-id`.

## Start

When `playback.allow_hevc_encoding` is enabled, video adaptation may encode
HEVC for clients that explicitly support HEVC on their HLS delivery route.
The server lists `video_to_hevc` in its validated transformation inventory
(`getAdminHardwareAcceleration`). The effective recipe and frozen transcode target identify
HEVC; existing H.264-only clients continue to receive H.264. Disabling the
setting affects new planning decisions without changing active frozen recipes.
HEVC direct play and remux remain independent of the encoding setting.

HEVC execution validates the selected hardware encoder on its assigned device.
When that encoder is unavailable, a validated software encoder can retain the
negotiated HEVC output. Hardware tone mapping can still perform the HDR-to-SDR
conversion before its frames feed the software encoder. An optional HEVC probe
failure removes HEVC availability without invalidating successful AAC or H.264
capability checks.

A bitrate cap is a ceiling on every encoder. A capped VAAPI encode forces the
VBR rate-control mode, or CBR where the assigned device offers only that, after
a cached one-frame check on that device; FFmpeg's automatic mode could choose
AVBR, which ignores the cap. When the device accepts neither mode, the encode
uses libx264 or libx265 in the same way, keeping hardware tone mapping where
the recipe uses it.

The body is the v3 start request plus `installation_id`. `file_id` and
`profile_id` are strings; `profile_id` must be the authenticated profile. Start
is idempotent on `playback_attempt_id` plus a digest of the request: replaying
the same body returns the stored decision, and the same attempt id with a
different body is `409 idempotency_conflict`. Replaying an attempt whose session
has ended (stopped, expired, or no longer live on this server) returns `201`
with `outcome: "adaptation_unavailable"`, `terminal.reason: "session_expired"`
and `terminal.retryable: true`; mint a new attempt. Local direct and HLS media
URLs in the plan are projected into the `/api/v2` namespace; the signed `st`
query they carry is unchanged.

A start that fails transiently answers `503` with `Retry-After`, not
`500 internal_error`, so the client retries instead of treating the failure as
a server defect. This covers a transport that could not start before the
request deadline, a terminal decision the plan store could not record on that
expired budget, and route-capacity exhaustion. Route capacity keeps the v1
`route_capacity_unavailable` body code; v2 maps it to `503 dependency_unavailable`
as the replan path does, and the other retryable start failures use
`dependency_unavailable` on both surfaces. A genuine internal fault remains
`500 internal_error`.

When the profile's stored `playback.subtitle_mode` is `always`, an omitted
`subtitle_track_id` / `subtitle_track_index` on start resolves to the stored
preference's track before planning, so subtitles are on — and a bitmap track
forces the burn-in video codec — on the first plan rather than after a
reconciliation replan. `subtitle_track_index: -1` is the explicit-off encoding:
it requests a one-off subtitle-free start without changing the stored mode, and
overrides `always`. Omission (the field absent) remains "no selection intent"
and is never read as off; a named track is never overridden; a stored mode other
than `always` (including the off state) leaves the selection to the client on
omission.

A start that returns a playable decision registers the device the client
declares in `X-Silo-Device-Id` (with the optional `X-Silo-Device-Name` and
`X-Silo-Device-Platform`) for the acting profile and refreshes its
`last_seen_at`, as described in the
[device registry](settings-api.md#device-registry). A refused start, or one that returns a terminal decision,
registers nothing. These headers do not change the playback device that
`X-Device-ID` names.

An omitted `audio_track_index` / `carried_audio_track_id` and omitted subtitle
selection are normally resolved by the server. A restarted start can carry the
previous attempt's explicit audio choice and embedded subtitle selection instead
of falling back to the profile default. Name the predecessor with the optional
`previous_playback_attempt_id` field: the server resolves that attempt from its
durable store, so the carry works when the predecessor session ran on another
replica or has already stopped. Both tracks are matched by identity — audio by
language, codec and layout; subtitles by language, codec and forced flag — because
the previous ordinal belongs to the previous candidate's transport. A selection
that cannot be matched is reported as a degradation warning rather than silently
planning the wrong track or subtitles off. A strict match can also warn when the
new inventory is less complete than the predecessor's (for example after a
virtual rotation with degraded probe data); operators should watch the warning
rate for this signature rather than treating every warning as a client error.

A client that already replays its own audio and subtitle selection needs no
carry. Without `previous_playback_attempt_id`, the legacy fallback matches exactly
one live session for the same profile and file on the same replica; two tabs or
devices behind one NAT address and user agent make that match ambiguous, so the
server carries nothing rather than guessing. The field is additive: older clients
that omit it keep the legacy behavior, and a client that always sends its own
selection is unaffected.

Selected track ids (`audio_track_id`, `subtitle_track_id`) embed the catalog row
the plan minted them against, which is the plan's `effective_media_file_id` and
can differ from the `file_id` a client re-sends on a restart. The server accepts
an echoed id when the embedded row names the same virtual candidate, or when it
is one of the same client's committed restart rows (the predecessor attempt's
requested/effective row or the matching live session's rows), and re-keys it onto
the row being validated. A row naming a different release is still treated as a
stale selection and degrades to the default pipeline.

Re-keying does not trust the carried ordinal positionally. Virtual resolution can
serve a different candidate than the row the request names, and that candidate
can order its tracks differently. The ordinal is therefore reused only when the
served inventory matches the inventory the identity was minted against; a
different inventory is re-identified by track identity (audio includes language,
codec, layout and titles) or reported as an audio/subtitle-unavailable
degradation warning instead of being applied to whichever track now occupies the
old position. When the request names `previous_playback_attempt_id`, that
attempt's captured plan inventory is the historical record even if the catalog
row under the same id was re-probed since; the mutable row is never treated as
proof of the old ordering. Without a predecessor id, the single matching live
session's durable attempt plan is read the same way, including the downloaded
subtitle tail a file-shaped inventory comparison cannot see. A downloaded
ordinal whose historical inventory cannot be proven — no attempt row, no
captured inventory, or a plan for another effective row — is cleared with the
degradation warning instead of being reused by position. A track_change
selection is minted against the current menu, so it is read through the
inventory that published it, including when the identity names another accepted
row, and then mapped onto the validated row without the predecessor ordering. A
re-key that cannot read the subtitle evidence fails retryably instead of
guessing from unverified evidence or leaving the old ordinal to land on another
track.

Client guidance for resume and restarts:

- Send `start_position` when the player already knows where to begin, or omit it
  so the server applies its own resume policy. Send an explicit `0` only for a
  deliberate start-over; the server must not reinterpret it as "resume".
- Do not start at zero and seek afterwards. That fetches the head of the
  timeline, then forces a second generation to be produced at the real position
  and costs the same seek recovery a direct start avoids.
- On a restart or reconnect, resend the viewer's audio and subtitle selection,
  or send `previous_playback_attempt_id` naming the attempt being replaced. An
  explicit selection is authoritative; the attempt id survives a client identity
  the server cannot match, including a different replica.

Two terminal reasons describe a source without stream metadata.
`source_metadata_incomplete` (`retryable: true`) means the file has not been
probed yet, or its probe lacks a field a route needs; trying again after the
scan can help. `source_unreadable` (`retryable: false`) means ffprobe ran and
rejected the file: it is empty, corrupt, or truncated, and no version of it was
ever probed successfully. A file the server cannot open or read (permissions,
storage I/O errors) is not reported this way and keeps
`source_metadata_incomplete`. Retrying cannot help until the file is replaced and
rescanned. The server records the rejection on the media file during a scan or
a playback-time probe repair, and a later successful probe clears it. The
rejection only refers to that file: with alternate versions allowed, the server
tries the item's other versions the viewer may play (library access and
playback-quality ceiling) before answering `source_unreadable`.
Season episode listings mark such files with `unreadable: true` (see
[Catalog API](catalog-api.md#episode-files)).

The web player retries an interrupted START with the identical body, including
when response headers arrived but reading the body failed. Its 60-second retry
budget includes backoff and response-body reads; each request gets at most
45 seconds so ordinary worker manifest startup can finish. Cancellation ends
the request or backoff. A 4xx refusal ends that start, and a later user Play
uses the newly selected file and a new attempt ID. The web client does not
restore pending START requests across page reloads.

Servers advertising `fixed_media_file_v1` accept the optional start field
`allow_alternate_versions`. Setting it to `false` keeps this attempt on the
requested file through quality, track, output, seek, and recovery replans.
Transcoding and delivery changes remain available for that file. If it cannot
play, the server returns an adaptation refusal instead of substituting another
version with a potentially different timeline. Omitted or `true` retains the
existing alternate-version behavior. The choice is persisted with the attempt
and cannot be relaxed by a replan.

When an administrator sets a positive bitrate limit for the stream's location
(local or remote), a new start allows the original file only when its probed
bitrate fits. Otherwise the server plans
a lower-bitrate transcode, including an audio and mux-overhead budget. A
client's lower bandwidth preference still wins. If no compliant encode route
exists, the server tries the item's other versions as it does for 4K and HDR
refusals. When none fits, the decision is terminal with
`bitrate_policy_unavailable`; it never falls back to the oversized original.
The ceiling measures encoded media rate, not HTTP fetch speed. Units are decimal
kbps. Original-file admission uses the catalog's integer kbps probe value, so
rounding can admit less than 1 kbps above an integer boundary. For a transcode,
let V be the selected video maxrate and A the selected audio target, in bits/s.
The encoder recipe budgets V + A below the location cap, including its mux
reserve, and sets video VBV capacity to 2V bits. Validation measures encoded
video/audio packets over their media timestamps, allowing the VBV burst:
`encoded bits <= (V + A)*T + 2V` over a contiguous media interval T. Audio packet
boundary rounding is allowed by one encoded audio frame at each end.
Container delivery is measured separately: for MPEG-TS allow up to 10% above
that encoded-packet envelope for packetization, headers and padding on the
supported synthetic validation fixture. This allowance is not a new network
quota or a promise for arbitrary container recipes. Segment download speed
may exceed the nominal media rate; a single two-second segment is not a steady
rate measurement. Recheck the complete encode and representative intervals,
and record codec, duration, video/audio recipe and container overhead.

The selected limit is frozen on a new playback attempt and reused through
replans, so later policy edits do not interrupt it.
`server_remote_stream_bitrate_policy_v1` and
`server_local_stream_bitrate_policy_v1` in `features` advertise this behavior.

The web Watch Party player requires this capability and sends `false`. When a
source cannot be adapted, `watch_party_source_fallback_v1` allows connected
viewers to request a shared replacement through the room's `source-fallback`
operation (see [Realtime API](realtime-api.md#room-membership-and-buffering)).
Every viewer then starts a new fixed-file attempt for that room selection. Ordinary
web, Apple, Android, and Jellyfin playback keep their existing behavior; Apple
has no active room caller and Android's room UI remains disabled.

## Version substitution

`startPlayback` and `replanPlayback` may commit a release other than the row the
client asked for: the requested release is dead or its provider listing failed, a
transport or decode failure rotated the candidate, or the planner could not
deliver the picked version to this client. The plan reports that instead of
leaving the client to diff ids:

- `substituted_from_file_id` is the catalog row the client asked for;
- `substitution_reason` is the machine-readable cause.

Both are omitted when the effective row is the requested one. The vocabulary is
additive — `dead_release`, `listing_failed`, `transport_failed`,
`decode_rejected`, `version_not_playable`, `unknown` — and a client must render
an unknown value as a generic substitution rather than failing. The fields are
UI-only: they are set after the plan identity is finalized and never change
`plan_id` or `plan_attempt_key`. `effective_media_file_id` still names the row
actually served, and `effective_virtual_uri` / `virtual_source_revision` follow
it. Every plan decision and every replan that commits another row carries them,
so a client never has to guess from an id diff.

A substitution is bounded by the viewer's selection. A substitute that would
offer fewer audio or subtitle tracks than the selected row, or contradict the
item's runtime, is refused. So is a substitute whose served bytes contradict the
release it claims: a row's own candidate may still play and carry its measured
inventory when a provider posts it short, but unverified bytes never adopt
another release's row, plan, or inventory. When no candidate can serve the
selection, the start answers the retryable `virtual_source_unavailable` terminal
with a message naming the version list, so the client offers the versions
instead of playing a poorer or different release.

A start's effective row is always one the item's version list carries, so the
row a client records as playing is a row the same client can show and reconcile.
A resolver substitution that lands on any other row is refused like an unusable
candidate and the next one is tried; when none qualifies the terminal names the
version list. Two cases are exempt: the requested row itself, and a
provider-neutral listing row, which owns no bytes and therefore binds to the row
that owns the served candidate as ordinary auto selection. A refusal for an
unlisted row is not a verdict about the release and never stamps it failed.

The refusal that reports served bytes contradicting the row answers the
retryable `source_mismatch` terminal reason, the same verdict the version list
publishes as `source_mismatch` (see [Catalog API](catalog-api.md)).
The requested release is alive; the provider served the wrong file. Like every
terminal reason, the value is additive: a client that does not recognize it
renders the message, which names the version list.

## Progress and stop

Progress accepts `{installation_id, sequence, position, is_paused}`. `sequence`
is a positive 64-bit integer the client allocates once per sample for the
session. The server applies it with a compare-and-set on the attempt row
(`UPDATE ... WHERE stopped_at IS NULL AND last_sequence < $sequence`), so a
higher sequence wins even when `position` moves backward, and no in-memory
session is required: any replica accepts progress for any live attempt. The
receipt is `{outcome, accepted?}`, where `accepted` is the latest committed
`{sequence, position, is_paused}`.

| Case | Result |
| --- | --- |
| Higher sequence than the last committed | 200 `outcome: "applied"` |
| Lower sequence | 200 `outcome: "stale_sample"`, `accepted` is the latest |
| Equal sequence, same payload | 200 `outcome: "replayed"` |
| Equal sequence, different payload | 409 `progress_conflict` |
| Session owned by another profile | 403 |
| Attempt ended by an administrator | 410 `playback_session_ended` |
| Attempt already stopped otherwise, or unknown | 404 |

An applied sample is persisted through the shared progress writers (resume
position, scrobbles), from the live session when this replica holds it and from
the attempt row otherwise. The writers run after the compare-and-set and the
resume position is last-write-wins, so after writing, the server re-reads the
row and rewrites if a newer sample landed meanwhile (on this or another
replica); the stored resume position ends at the row's latest sample. A
replayed sample persists again, since the client retried because the first
reply was lost. An attempt started with `progress_persistence: "client"`
records no resume position; multi-part audiobooks use it and report their
global position through `POST /api/v2/sync/progress`.

The progress response is an acknowledgement, not a long-poll or a state feed.
Post-CAS side effects run under a bounded budget, so a slow writer cannot hold
the receipt; keep the heartbeat at its normal cadence and never derive pause or
play state from how long the receipt took. A pause/resume transition is emitted
before the retryable user-store write and the admin session sync, so a slow sync
cannot consume the budget and drop the one-shot scrobble.

Stop accepts `{installation_id, stop_id}` plus an optional final sample
`{sequence, position, is_paused}`; `sequence` and `position` appear together or
not at all. `stop_id` is a canonical UUID the client mints once and keeps on
every retry; a `stop_id` the server mints for its own ends (below) is refused
with `400`. The first stop wins a compare-and-set on `stopped_at`: it applies
the final sample when it is newer than the last progress, runs the stop and
history writer, stops the local session and transcode, writes the stream deny
marker and stores the receipt on the row. The reply is
`200 {outcome: "stopped", stop_id, accepted?, history_id?}`; `history_id` is
present only when a watch-history row was created. Every later DELETE for the
session, with the same or a different `stop_id`, is
`200 {outcome: "replayed", ...}` carrying the stored receipt. There is no
`202`, no draining state and nothing to poll.

The web player retains the exact STOP body in memory for later retries. Each
stop call allows up to 30 seconds for pending progress, then a separate
30-second delivery budget. Backoff cannot dispatch a DELETE after that budget
expires. A page reload discards this in-memory retry state.

A session that expires or is aborted server-side is stopped under a
server-minted `stop_id` (a UUID v5 of the session id, one for each of expiry
and abort), so a later client stop replays and a start replay reports
`session_expired`. Every way a session ends, on any API server, stops the row
and claims the receipt's finalization before it writes watch history, provider
events or the admin log; an end that loses that race writes none of them and
only releases what its own server holds. An end that finds the row stopped by
an end whose server died before finishing it, and whose claim has lapsed,
finishes it as that end, with its provider events; an expiry of a copy on
another server does the same. The admin log records the reason of the stop
that won the row.

A session an administrator ends without the player confirming it (a stop,
pause or resume the player did not answer before its deadline, or a
terminate) is stopped under a different server-minted `stop_id`. Progress and
replan for that session answer `410 playback_session_ended` instead of `404`.
A player that gets this answer leaves playback: it must not start a fresh
session at the same position, which is how it recovers from the `404` of an
expired session or a server restart. A stop the client made first keeps its
own receipt.

## Replan and route events

`replanPlayback` covers failure recovery, seek re-anchor, and track, quality and
output changes, with the v3 body plus `installation_id`. It is idempotent on `replan_request_id`
plus the body digest: the same request replays its committed decision; a reused
id with different input, a superseded plan, or an identical replan still in
progress is `409`; a session owned by another profile is `403`; a session an
administrator ended is `410 playback_session_ended`; any other ended session is
`404`.

Route events take the v3 event plus `installation_id` and a client-minted
`event_id`. `202` acknowledges queueing, not a durable write. A retry with the
same `event_id` is recorded once (partial unique index on attempt and event
id); clients never retry automatically and treat `429` as drop.

A replan response also carries the additive `playback_plan.delivery_change`
marker, described in the [protocol spec](architecture/playback-protocol-v3.md#92-mid-session-delivery-change):
it is present only when the replan changed the serving route mid-session, and
names the old and new `delivery` and `play_method` so a client can tell a
planned route change from a silent swap. It is absent on a start and on a seek
reanchor, and is excluded from plan identity.

## Delivery

| Operation | Method and path |
| --- | --- |
| Original bytes | GET/HEAD `/api/v2/stream/{session_id}` |
| HLS manifest | GET/HEAD `/api/v2/playback/transcode/{session_id}/master.m3u8` |
| HLS segment | GET `/api/v2/playback/transcode/{session_id}/segment/{name}`; `sub_NNNNN.vtt` names a subtitle rendition segment, `sub_<track>_NNNNN.vtt` one of an on-demand rendition (`hls_subtitle_renditions_all_v1`) |
| Subtitle sidecar | GET/HEAD `/api/v2/stream/{session_id}/subtitles/{track}` |
| Subtitle fonts | GET `/api/v2/stream/{session_id}/subtitles/{track}/fonts`, JSON `{items: [{name, data}]}` |

A font bundle that is still being extracted answers an empty collection with
`Cache-Control: no-store` and `X-Vio-Font-Bundle-Pending: true`; the client
falls back to default fonts and re-fetches. When the complete attached pack is
past the 48 MiB ceiling and script-driven subsetting cannot produce a bundle
(the script is unreadable, more than eight tracks, more than 8 MiB of decoded
script, or no referenced attachment matched), the response also carries
`X-Vio-Font-Bundle-Fallback: subset_unavailable`. That marker means the server
has answered for this source and the client is on system fonts; it is not a
still-running extraction. A definitive empty bundle for a file with no
attachments keeps the cacheable response and carries neither marker.

These reuse the shared byte-delivery handlers behind the v2 listener, as the raw
playback registry allows. The plan's URLs
carry the signed stream reference `st`; a media element that cannot set headers
may send the account bearer as the `token` query parameter, and the viewer
headers select the profile. Without an account credential, `st` on its own
authorizes the media, sidecar and HLS requests of the one session it names, as
its account and profile: a Google Cast receiver gets only the URL. Viewer
authorization still runs for that profile. The reference is bound to the login
session that started playback, and the session's playback attempt must still be
live, so stopping the session, signing out or disabling the account ends it
before its 24-hour expiry. A session started with an API key keeps needing the
account credential. After a restart the session is rebuilt from the token the
client re-presents. Plans that negotiated `header_authenticated_media_v1` carry
no `st`; the API rebuilds those from its Redis recovery card for the
authenticated owner, while the card exists
([restart-resilient playback](architecture/restart-resilient-playback.md)).
Ranges, conditional requests and HEAD keep their HTTP semantics; an
unsatisfiable range is `416 range_not_satisfiable`. A `server_remux_progressive`
stream is the exception: it is one forward-only response from its `seek`
origin, so it answers `200` from that origin whatever the `Range`. A client that
lists `progressive_range_refusal_v1` in `deliveries.progressive.features` gets
`remux_range=refuse` on that URL, and a GET whose range starts past byte 0, or
is a suffix range such as `bytes=-500`, is then `409` before the remux starts;
the client reopens the stream at its current position. The API answers it as a
v2 `conflict` problem and a proxy as plain text, so a client should act on the
status. A proxy that does not advertise `remux_range_refusal_v1` would ignore
the parameter, so the plan leaves it off that proxy's URL and the stream keeps
the `200`. The web video player opts in on Chromium browsers only. The web
audiobook player, which has no reopen at the current position, Firefox, Safari
and native players keep the `200`; native players skip the bytes they already
have. A failure before the first
byte is a v2 problem; a failure after it ends the stream. A stopped, expired or
terminated session answers `410 playback_session_ended`. A subtitle sidecar the
node has no room to read yet answers `503` with `Retry-After`; retry it then.

A progressive remux commits its body only after FFmpeg produces a media byte.
If no byte arrives within 15 seconds (a provider candidate that accepts the
connection but never serves), the server closes the process and answers a
pre-body failure (502 / retryable dependency problem) instead of holding the
response. On the v2 virtual-candidate stream the failure is deferred so the
handler can retry a cached copy or fail over to a sibling release first; that
502 is what remains once failover is exhausted. Relays and other non-deferred
paths answer it directly. Clients should treat it as retryable: retry or replan
to another candidate rather than waiting for the stream to start.

A transport failover that lands on a genuinely different release probes that
replacement before starting it. The viewer's audio selection is re-identified on
the replacement's inventory by the concrete language it carries (the same
language-carriage rule a version switch uses); codec and channel layout may
differ between releases and are not required to match. When no track carries
that language, the fallback starts on the replacement's default and reports an
audio-unavailable degradation warning instead of silently resetting. When the
replacement's inventory could not be measured at all, the requested ordinal is
passed through unverified: the plan reports `audio_track_unverified` and sets
`audio_selection_pending: true`, because the ordinal may name another stream or
no stream. A client must render that audio pick as pending/unverified rather
than confirmed while the flag is set, and must not treat it as a known
degradation to the file's default (`audio_track_unavailable`). A client that
negotiated `audio_track_unverified_v1` can rely on the flag; older v2 clients
still see the warning and are otherwise unchanged. The marker is outside plan
identity and never changes stream selection. Jellyfin-protocol clients are
unaffected: that surface has no degradation channel and never takes this
cross-release fallback.

A subtitle selection is carried across a candidate rotation by identity. The
track source (external sidecar, embedded, or downloaded), the output muxer class
(WebVTT, ASS, or PGS), the canonical language, the forced flag, and the
hearing-impaired flag are hard gates; the normalized codec and the displayed
title only break ties, and two tracks that still cannot be told apart fail
closed rather than picking one. A downloaded subtitle resolves by its stable
row id first and then by language and format, never by title. A unique match
moves the selection to the rotated release's ordinal and is silent when both
tiebreaks agree; a match that differs on a tiebreak is kept and reported as a
`subtitle_carry_warning` degradation warning. No match clears the selection,
starts with subtitles off, reports `subtitle_track_unavailable`, and records the
rotation's from/to candidates.

The rotation commit records the carried ordinal with the new candidate
generation. A subtitle URL minted before the rotation is therefore answered on
the carried ordinal, and a second rotation re-keys from that position. When the
rotation could not preserve the selection, the old ordinal is terminal: the
subtitle fetch answers the retryable `409 subtitle_source_changed` with a
`Retry-After` header instead of resolving the dead candidate or spawning an
extraction. The client replans and re-selects from the rotated inventory; it
keeps the requested subtitle identity in its own state meanwhile.

A seek is one server operation. Issue a single media request for the new
position; sending the same seek twice (or aborting and immediately re-sending an
identical request) spawns a second remux/transcode for the same position and
doubles encoder work. Retry only after a response, a timeout, or a client-side
failure verdict.

Segment URLs the manifest exposes carry an opaque `sgen` generation token. A
segment request whose token names a generation the server no longer serves —
neither the live FFmpeg timeline nor a retained switchover predecessor — answers
`412 stale_generation`; the client reloads the manifest and re-addresses the
segment to the current generation. A request without `sgen`, minted before the
token existed, keeps the legacy behavior.

A `412` on a segment is a playlist-staleness signal, not a retryable fragment
error: retrying the same URL with the same `sgen` cannot succeed. Refetch the
playlist (or the manifest URL the client loaded) and retry the same position
with the new URLs. Do not count the `412` against the fragment retry budget and
do not tear down the session for it.

A segment request for a generation whose decoder rejected the source answers
`422 decode_failed`, the same permanent verdict the manifest path publishes;
replan to another candidate instead of retrying the segment.

An in-place restart (a far seek or a switchover) rotates the generation but
continues the same absolute segmented timeline. Only the exact generation token
displaced by the most recent restart is served from the live generation, and
only when that restart kept the emitted-byte recipe; segment numbers alone do not
prove codec, track or init compatibility. A request at or after the restart point
continues; a segment before the restart point, an older generation's token, or a
recipe-changing restart answers `412`. This keeps a client that has not yet
refetched the playlist playing through the restart without mixing generations.
The `init.mp4` segment is generation-independent for an unchanged recipe and is
served through the rotation too.

A far-seek segment request that schedules a restart is held for the restarted
generation's output and then served. A 6-second ceiling, below the 8s default
HTTP read timeout of ExoPlayer-based clients, bounds the restart phase from the
start of the request's recovery: a stalled-head wait before the restart draws
from the same ceiling, so the continuation only gets the remainder, and the
produced-head watchdog during the continuation is 3 seconds. The API server and
a remote transcode node use the same continuation logic, so a cluster seek
behaves like a single-node seek. Clients should let that request finish; if the
ceiling expires it answers `412` while the restart keeps producing, so refetch
the playlist as above.

A plan may carry `stream.parallel_range_url`: a URL the client may read the
file's byte ranges from over several connections. The server sets it only for an
`original_http` plan whose attempt sent client feature
`parallel_range_origin_v1` without `header_authenticated_media_v1`, and names
one of three:

- the plan's own stream, as a reference relative to the API root like
  `stream.url`, when the API serves the file. The server names it only for an
  attempt a login session started;
- the same stream on the media origin the administrator set in
  `server.web_media_url`, in place of the first, when one is set and the client
  did not arrive through a network access provider;
- the proxy node's own URL, the one `stream.url` already carries, when a node
  serves the file.

The URL authorizes itself (`st`, or the token in a node URL's path). A client
uses it exactly as given and adds no account token, header or cookie to it. A
client reads parallel ranges only over connections that are separate: on the
page's own origin, the web player does so only when the page loaded over
HTTP/1.1, and it stops when the browser reports that a range came over HTTP/2
or HTTP/3 (the stream answers carry `Timing-Allow-Origin: *` for that). A plan
without `parallel_range_url` is read from `stream.url`.
`stream.url` stays valid; a client may also read the warm head, overflow ranges,
or a natively played file from it.

Three kinds of response are readable from another origin. Each carries
`Access-Control-Allow-Origin: *` and no `Access-Control-Allow-Credentials`:

- The original-file stream, `GET` and `HEAD` on `/api/v2/stream/{session_id}`,
  refusals included, with `Range` allowed and `Content-Range` exposed. The web player reads it from
  the media origin. The `OPTIONS` preflight answers `204` before any
  credential is read, with `GET, HEAD, OPTIONS` and the `Range` header allowed,
  and `Access-Control-Max-Age: 600` so a browser that preflights `Range` does
  not repeat it for every range.
- The HLS manifest and segment answers, refusals included, with `Range` allowed
  and the range and validator headers exposed, so a player such as a Google Cast receiver can
  read them. The API answers no `OPTIONS` preflight there; Cast receivers
  fetch playlists and segments with plain GETs and have not been seen to send
  one.
- Subtitle sidecars, from their handler. A refusal before the handler runs
  carries no CORS header.

Subtitle inventory URLs in v2 playback responses use the `/api/v2/stream/`
mount, including relative `/stream/` values produced by the shared inventory
builder. The web player applies the same mapping to realtime subtitle URLs.
Absolute delivery URLs from distributed nodes retain their origin and path;
signed query strings are preserved without decoding or re-encoding.

### Sidecar identity

A version's `subtitle_tracks` publishes external sidecars with their basename
in `file_name` and an opaque `path_key` — a stable SHA-256 hash of the full
sidecar path. The key lets a client keep two sidecars that share a basename in
different directories distinct without publishing the server filesystem path;
it mirrors the server's own sidecar de-duplication discriminator. `path_key` is
absent on embedded tracks and on servers that predate it: a client falls back
to `file_name` there, which cannot separate same-basename sidecars.

An external subtitle whose only title is its file name is labeled by that
name only for a viewer who may see file names (see
[File paths and names](catalog-api.md#file-paths-and-names)). Everyone else
gets its embedded title or language as the `label`, numbered like the catalog
title when several share a language, format and flags. Realtime subtitle
events never label a sidecar by its file name, for any viewer.

## Revocation

Stop, session expiry or abort, and admin stop or terminate write the
Redis key `silo:streamauth:<session_id>` with a TTL equal to the stream token
lifetime. The API checks it before it serves or reconstructs a session, the
proxy checks it on every token-verified serve, and every relay to a transcode
node checks it before forwarding, as the API does before a node rebuilds a
session, so a still-valid token cannot revive a session on any replica. Lookups are cached in
process for two seconds. When Redis is unavailable the check fails open with a
warning: serving never waits on Redis. A deployment without Redis keeps the
token TTL as its bound.

The realtime control socket (`/api/v2/playback/sessions/{session_id}/control/ws`)
is documented in the [realtime API](realtime-api.md); ownership is the session's
account and profile.

## Shuffle

A shuffle plays random movies and episodes from one scope until the profile
stops. The server picks every item, so any client or API process can continue
a shuffle another one started.

| Operation | Method and path | Success |
| --- | --- | --- |
| `getShuffleCapability` | GET `/api/v2/shuffles/capabilities` | 200 capability state and `scope_kinds` |
| `createShuffle` | POST `/api/v2/shuffles` | 201 shuffle, `Location` |
| `getShuffle` | GET `/api/v2/shuffles/{shuffle_id}` | 200 shuffle |
| `advanceShuffle` | POST `/api/v2/shuffles/{shuffle_id}/advance` | 200 shuffle |
| `skipShuffleItem` | POST `/api/v2/shuffles/{shuffle_id}/skip` | 200 shuffle |
| `deleteShuffle` | DELETE `/api/v2/shuffles/{shuffle_id}` | 204 |

`createShuffle` takes `{scope: {kind, id}}`. `kind` is `library` (a library
ID; movie, TV, and mixed libraries), `series` or `season` (a content ID,
including the `<series>-S<number>` IDs of seasons without stored metadata), or
`library_collection` or `user_collection` (a collection ID). A collection plays
its movies, its series' episodes, and any episodes it lists itself. Specials
play like any other episode.
A scope the profile cannot see is `404`. A library with no movies or episodes,
or a scope with nothing the profile can play, is `409 conflict`.

A shuffle is `{id, scope: {kind, id, title, parent_title?}, current, next,
created_at, updated_at}`. `current` and `next` are catalog item cards; `next`
equals `current` only when one item can play. `title` names the scope when the
shuffle started, and `parent_title` is a season's series title.

Picks never repeat an item until every playable item in the scope has played.
The next cycle never starts with the item that just played. Each pick
re-applies the profile's library access, file access, playback quality ceiling,
and parental limits. An item needs a present file in an enabled library.

Play `current`. When it ends, call `advanceShuffle` with `from_content_id` set
to the item that played: `next` becomes `current` and a new `next` is picked.
`skipShuffleItem` with `next_content_id` replaces the announced `next` with
another pick. The skipped item never played, so it stays in the cycle and can
come up later; when it is the only item the cycle has not played, it stays
`next`. Advance and skip act only while the named item still holds that
position, so a retry after a lost response returns the same shuffle unchanged.
A retried `createShuffle` starts a second shuffle; the first is never read
again.

If the announced `next` can no longer play when the shuffle is read or moves
on, because its file went missing or the profile lost access, another pick
replaces it. When nothing in the scope can play any more, `getShuffle` answers
`409 conflict`. A shuffle belongs to the profile that started it, and every
operation re-checks that the profile can still see its scope; another
profile's shuffle, or one whose scope the profile lost, is `404`. There is no
separate expiry job: whenever a new shuffle is created, shuffles untouched for
seven days are deleted.

The web client carries the shuffle in the watch URL as `?shuffle=<id>` and
plays every pick from the beginning, ignoring saved progress. While a shuffle
is set, the post-roll screen shows `next` instead of the next episode, and the
player offers no sequential next or previous episode.

## Marker preview delivery

Native v2 playback capabilities also advertise `marker_thumbnails_v1`. The web
player opts into that feature at playback start to receive `marker_thumbnail_ready`
updates; negotiation stays fixed for the attempt. Canonical watch reads return
the same durable optional preview fields on `marker_segments`, so reconnects
and clients without the realtime feature can refresh through the normal read.

A marker preview is captured at its start timestamp. It does not replace an
embedded chapter, change seek boundaries, or enable timeline trickplay. Native
clients can ignore the optional preview fields while keeping chapter navigation.
Jellyfin chapter names, ticks and image mappings continue to represent embedded
chapters; marker images are not projected as invented Jellyfin chapters. Offline
marker inventories do not include these remotely served preview images.

## Buffering quality (native v2)

`buffering_quality_v1` adds the start and replan fields, learned limits and
`playback_plan.bandwidth_limit` described in
[playback protocol v3](architecture/playback-protocol-v3.md#buffering-quality-native-v2).
These operations read and reset what the server keeps for the calling device.
They are profile-scoped and identify the device by `X-Silo-Device-Id`; they
need no installation ID or body. Without a device ID nothing is stored, and the
reads find nothing.

`GET /api/v2/playback/connection` returns the state for the connection the
request arrives on: `connection` (`local`, `remote` or `provider:<name>`) and
nullable `learned_limit: {kbps, observed_at, expires_at}`. With
`connection_speed_v1` it adds optional
`probe: {kbps, lower_bound, measured_at, transport}`, the latest test, and
optional `playback_sample: {kbps, measured_at}`, the best recent playback
throughput, a lower bound. Both are returned until row retention removes them;
clients judge them against `bandwidth_probe_max_age_seconds`. It also adds
`auto_budget_kbps`: 80% of the lower of the reading and the unexpired learned
limit, then capped by the administrator limit, where the reading is the higher
of the probe and the sample within their lifetime. Without either observation
the budget is absent.
`DELETE /api/v2/playback/connection/learned-limit` clears that connection's
limit and returns 204. It requires `X-Silo-Device-Id`; a reset without one is
a `422` validation problem, since it would clear nothing.

`POST /api/v2/playback/bandwidth-probe/results` (`connection_speed_v1`)
accepts `{kbps, bytes, duration_ms, transport, lower_bound, manual}` and
returns 204. Bounds are 100–1,000,000 kbps (those of
`bandwidth_estimate_kbps`), 1–1,000,000,000 bytes and 1–60,000 milliseconds;
`transport` is `wifi`, `ethernet`, `cellular` or `other`; the optional
`lower_bound` (default `false`) marks a result that proves only "at least
`kbps`". The optional `manual` (default `false`) marks a test the viewer
started with **Test speed**. A manual result clears the connection's
`playback_sample` in the same write, so the reading becomes the test's result
rather than an older, higher sample; one above the learned limit for the
device and connection also clears the limit, as the reset does. An automatic
result keeps the sample. The server keeps the latest result per device and
connection.

A `plan_summary` route event may carry the diagnostics key `throughput_kbps`,
the rate the plan's media requests received data. The server keeps the highest
per device and connection as `playback_sample`, replacing it with a later one
that is higher or once it is older than the reading lifetime. A manual probe
result clears it.

The admin device detail adds optional `connections`. Each entry has `id`, an
opaque connection identifier, `connection`, nullable `probe` (with
`lower_bound`), nullable `playback_sample`, nullable `learned_limit` and
nullable `plan_summary: {plan_id, reported_at, diagnostics}`.
`DELETE /api/v2/admin/devices/{user_id}/{device_id}/connections/{connection_id}/learned-limit`
needs acting-admin authority and returns 204. Both resets clear only the
learned limit and keep the probe and plan summary.
