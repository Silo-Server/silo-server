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
| `reportPlaybackRouteEvent` | POST `/api/v2/playback/route-events` | 202 `{event_id, outcome: "accepted"}` |

## Capabilities

The response is `{installation_id, revision, state, allowed, protocol_versions,
features, deliveries}` with `Cache-Control: private, no-cache` and an `ETag`; clients may use `If-None-Match` and receive `304 Not Modified` when unchanged. `state` is `available`
and `allowed` is `true`; a server without playback wired answers
`not_configured` with `allowed: false`. `installation_id` is the persisted
server instance UUID that diagnostics also report. `protocol_versions` is
`[3]`. `features` is the v3 server feature set plus `sequenced_progress_v1`,
`fixed_media_file_v1`, `marker_segments_v1`, and `trickplay_v1`. When the room service and
authenticated room socket are configured, it also includes
`watch_party_source_fallback_v1` and `watch_party_coordinator_v1`.
The coordinator capability covers shared room
membership, command-aware readiness, and ready/buffering/syncing member status.
`deliveries` lists `original_http`, `server_remux_progressive`,
`server_remux_hls` and, when transcoding is enabled, `server_transcode_hls`.
`revision` is a digest of the rest.

Every mutation body carries the `installation_id` the client read from
capabilities. A different value is `409 installation_changed`: refresh
capabilities and start a new attempt. There is no admission step and no
per-account enrollment.

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
geometry, and revision retention.

## Start

When `playback.allow_hevc_encoding` is enabled, video adaptation may encode
HEVC for clients that explicitly support HEVC on their HLS delivery route.
The server advertises `video_to_hevc` through the existing transformation
capability inventory. The effective recipe and frozen transcode target identify
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
[Catalog API](catalog-api.md#episode-files)). The v1 start route shares the
planner, so v1 clients can also receive `source_unreadable`; like any unknown
reason, they show `terminal.message`.

The web player retries an interrupted START with the identical body, including
when response headers arrived but reading the body failed. Its 60-second retry
budget includes backoff and response-body reads; each request gets at most
45 seconds so ordinary worker manifest startup can finish. Cancellation ends
the request or backoff. A 4xx refusal ends that start, and a later user Play
uses the newly selected file and a new attempt ID. The web client does not
restore pending START requests across page reloads.

The frozen v1 start route ignores `allow_alternate_versions`.
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
| Attempt already stopped or unknown | 404 |

An applied sample is persisted through the same writers v1 uses (resume
position, scrobbles), from the live session when this replica holds it and from
the attempt row otherwise. The writers run after the compare-and-set and the
resume position is last-write-wins, so after writing, the server re-reads the
row and rewrites if a newer sample landed meanwhile (on this or another
replica); the stored resume position ends at the row's latest sample. A
replayed sample persists again, since the client retried because the first
reply was lost. An attempt started with `progress_persistence: "client"`
records no resume position; multi-part audiobooks use it and report their
global position through `POST /api/v2/sync/progress`.

Stop accepts `{installation_id, stop_id}` plus an optional final sample
`{sequence, position, is_paused}`; `sequence` and `position` appear together or
not at all. `stop_id` is a canonical UUID the client mints once and keeps on
every retry. The first stop wins a compare-and-set on `stopped_at`: it applies
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
server-minted `stop_id` (UUID v5 of the session id), so a later client stop
replays and a start replay reports `session_expired`.

## Replan and route events

`replanPlayback` covers failure recovery, seek re-anchor, and track, quality and
output changes, with the v3 body plus `installation_id`. It runs the same
application code as the frozen v1 replan route. It is idempotent on `replan_request_id`
plus the body digest: the same request replays its committed decision; a reused
id with different input, a superseded plan, or an identical replan still in
progress is `409`; a session owned by another profile is `403`; an ended
session is `404`.

Route events take the v3 event plus `installation_id` and a client-minted
`event_id`. `202` acknowledges queueing, not a durable write. A retry with the
same `event_id` is recorded once (partial unique index on attempt and event
id); clients never retry automatically and treat `429` as drop.

## Delivery

| Operation | Method and path |
| --- | --- |
| Original bytes | GET/HEAD `/api/v2/stream/{session_id}` |
| HLS manifest | GET `/api/v2/playback/transcode/{session_id}/master.m3u8` |
| HLS segment | GET `/api/v2/playback/transcode/{session_id}/segment/{name}` |
| Subtitle sidecar | GET/HEAD `/api/v2/stream/{session_id}/subtitles/{track}` |
| Subtitle fonts | GET `/api/v2/stream/{session_id}/subtitles/{track}/fonts`, JSON `{items: [{name, data}]}` |

These reuse the shared byte-delivery handlers behind the v2 listener, as the raw
playback registry allows. The plan's URLs
carry the signed stream reference `st`; a media element that cannot set headers
may send the account bearer as the `token` query parameter, and the viewer
headers select the profile. Account authentication and viewer authorization are
always required. After a restart the session is rebuilt from the token the
client re-presents
([restart-resilient playback](architecture/restart-resilient-playback.md)).
Ranges, conditional requests and HEAD keep their HTTP semantics; an
unsatisfiable range is `416 range_not_satisfiable`. A failure before the first
byte is a v2 problem; a failure after it ends the stream. A stopped, expired or
terminated session answers `410 playback_session_ended`.

Subtitle inventory URLs in v2 playback responses use the `/api/v2/stream/`
mount, including relative `/stream/` values produced by the shared inventory
builder. The web player applies the same mapping to realtime subtitle URLs.
Absolute delivery URLs from distributed nodes retain their origin and path;
signed query strings are preserved without decoding or re-encoding.

## Revocation

Stop, session expiry or abort, and admin stop or terminate (v1 and v2) write the
Redis key `silo:streamauth:<session_id>` with a TTL equal to the stream token
lifetime. The API checks it before it serves or reconstructs a session, and the
proxy and transcode nodes check it on every token-verified serve, so a
still-valid token cannot revive a session on any replica. Lookups are cached in
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

## v1 bridge

`/api/v1/playback/start`, `/{session_id}/progress`, `DELETE /{session_id}`,
`/{session_id}/replan`, `/route-events` and `/api/v1/stream/...` keep their
frozen handlers and bodies: progress is `{position, is_paused}` and answers
`204`, and stop needs no body and answers `204`. They share the application
code, the deny marker and the attempt row with v2. Apple and Android use this
surface until they adopt v2; it is retired with the rest of `/api/v1` under the
`410 client_upgrade_required` tombstone
([API contract](architecture/api-contract.md)).
