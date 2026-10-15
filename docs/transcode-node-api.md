# Transcode node start contract

The API-to-node `POST /transcode/start` request and response. How nodes
authenticate, read their sources and rebuild lost sessions is in
[transcode nodes](architecture/transcode-nodes.md); the wire descriptions are in
the `x-silo-worker-protocols` extension of `contracts/api/v2/openapi.json`.

## Source and identity

Every start carries `source_grant`, the core's grant for the job's source file.
The node opens its input with it through the core
(`POST /api/internal/nodes/self/sources/open`) and refuses a start without one.
`input_path` is informational: the node never opens a path it was sent, only the
path or relay the core approves for the grant.

`session_id` is the transport id the node serves the job under.
`playback_session_id`, `user_id`, `profile_id` and `media_file_id` name the
playback session the job serves. The node files its session record and stream
telemetry under them; it reads no stream token. A session the node lost, and
every progressive `/remux` request, get the same identity, the recipe and a
fresh grant from `POST /api/internal/nodes/self/resolve` instead.

The same `source_grant` field is on the download preparation, trickplay and
media sample requests and on `chapterthumbs.RemoteExtractRequest`. Download
preparation leaves it out of `ExecutionFingerprint`, so artifact identities do
not change.

## Throttle

The API server resolves `enable_transcode_throttle` and
`transcode_throttle_seconds` before starting remote HLS playback. The
`POST /transcode/start` request carries `throttle_seconds`: zero disables
throttling; a positive value sets the forward buffer in seconds. Configured
positive values below 60 seconds are clamped to 60, and invalid configured
values use the 300-second default. Negative request values return HTTP 400.

The start response echoes `throttle_seconds` after arming the throttler.
When throttling is enabled, the API rejects a missing or mismatched echo and
stops the rejected remote transport. Disabled requests remain compatible with
nodes that omit this field. Throttling is on unless an administrator turns it
off. Every 1.0 node echoes the field; alpha nodes cannot run against 1.0 API
servers at all, so nodes are stopped before the API servers are upgraded and
started again after (see the upgrade order in [update to 1.0](update-to-1.0.md)).

Recipe cards preserve the resolved threshold. Remote reconstruction, which runs
the recipe card the core resolves, and FFmpeg restarts re-arm the same policy. Local native
and Jellyfin compatibility playback share the settings resolver. Playback
expiration closes owned transports; process shutdown drains local FFmpeg
sessions, cancels and waits for active progressive remux requests, and refuses
new HLS or progressive-remux admission after the drain begins.

Copy-video playlists retain actual FFmpeg fragment durations. A complete
playlist inferred from source keyframes is not safe: restarting the HLS muxer
can change its cut schedule, giving an existing segment URL different content.
Clients seeking outside the produced window must negotiate a new playback
transport at the requested source position.

The throttle fields belong to the internal API-to-node contract. Apple and
Android clients require no request or response changes.

When the API relays a transcode whose node it can no longer reach, it moves
the transcode to another node with an ordinary `POST /transcode/start`: the
same `session_id` (transport id) and playback session identity, the recorded
recipe, a fresh `source_grant` for the new node, `throttle_seconds` from that
recipe, `require_ready`, and a start position resumed at the requested
segment. The response is checked with the same attestations as a fresh start,
and a node that fails them is stopped and skipped. A source the catalog no
longer lists at the recipe's path ends the move before any start. The API then
sends a best-effort `DELETE /transcode/{session_id}` to the node it left, and
stores the recipe naming the new node under the transport id: the client's
stream token, which the relay keeps forwarding, still names the node it left,
and the core's resolve prefers the stored recipe, so only the new node can
rebuild the transport after a restart. See
[Transcode node lost under the API relay](architecture/restart-resilient-playback.md#transcode-node-lost-under-the-api-relay).

## Acceleration report

The start response also reports `hw_accel`, the backend retained for video
decoding and tone mapping, and optional `encoder_hw_accel`, the actual video
encoder backend. When a GPU can tone-map but cannot encode HEVC, `hw_accel`
keeps that GPU backend while `encoder_hw_accel` is `none` for libx265 encoding.
The same holds when a VAAPI device tone-maps a capped encode but offers neither
VBR nor CBR rate control; libx264 or libx265 then encodes.
Activity reporting uses `encoder_hw_accel`, falling back to `hw_accel` for
older nodes that omit it. Stored recipe cards preserve both values; execution
validates the encoder again when reconstructing a session.
