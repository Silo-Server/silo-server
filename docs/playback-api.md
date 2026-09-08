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
features, deliveries}` with `Cache-Control: no-store`. `state` is `available`
and `allowed` is `true`; a server without playback wired answers
`not_configured` with `allowed: false`. `installation_id` is the persisted
server instance UUID that diagnostics also report. `protocol_versions` is
`[3]`. `features` is the v3 server feature set plus `sequenced_progress_v1`.
`deliveries` lists `original_http`, `server_remux_progressive`,
`server_remux_hls` and, when transcoding is enabled, `server_transcode_hls`.
`revision` is a digest of the rest.

Every mutation body carries the `installation_id` the client read from
capabilities. A different value is `409 installation_changed`: refresh
capabilities and start a new attempt. There is no admission step and no
per-account enrollment.

## Start

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
the attempt row otherwise. An attempt started with `progress_persistence:
"client"` records no resume position; multi-part audiobooks use it and report
their global position through `POST /api/v2/sync/progress`.

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

A session that expires or is aborted server-side is stopped under a
server-minted `stop_id` (UUID v5 of the session id), so a later client stop
replays and a start replay reports `session_expired`.

## Replan and route events

Replan runs the same application as `/api/v1/playback/{session_id}/replan`:
failure recovery, seek re-anchor, and track, quality and output changes, with
the v3 body plus `installation_id`. It is idempotent on `replan_request_id`
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
| Subtitle fonts | GET `/api/v2/stream/{session_id}/subtitles/{track}/fonts`, JSON `[{name, data}]` |

These are the v1 delivery handlers behind the v2 listener. The plan's URLs
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

## v1 bridge

`/api/v1/playback/start`, `/{session_id}/progress`, `DELETE /{session_id}`,
`/{session_id}/replan`, `/route-events` and `/api/v1/stream/...` keep their
frozen handlers and bodies: progress is `{position, is_paused}` and answers
`204`, and stop needs no body and answers `204`. They share the application
code, the deny marker and the attempt row with v2. Apple and Android use this
surface until they adopt v2; it is retired with the rest of `/api/v1` under the
`410 client_upgrade_required` tombstone
([API contract](architecture/api-contract.md)).
