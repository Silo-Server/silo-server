# Transcode node throttle contract

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
off, so update transcode nodes before the API servers that require this
attestation.

Recipe cards and signed reconstruction claims preserve the resolved threshold.
Remote reconstruction and FFmpeg restarts re-arm the same policy. Local native
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
same `session_id` (transport id), the recorded recipe, `throttle_seconds` from
that recipe, `require_ready`, and a start position resumed at the requested
segment. The response is checked with the same attestations as a fresh start,
and a node that fails them is stopped and skipped. The API then sends a
best-effort `DELETE /transcode/{session_id}` to the node it left. See
[Transcode node lost under the API relay](architecture/restart-resilient-playback.md#transcode-node-lost-under-the-api-relay).

The start response also reports `hw_accel`, the backend retained for video
decoding and tone mapping, and optional `encoder_hw_accel`, the actual video
encoder backend. When a GPU can tone-map but cannot encode HEVC, `hw_accel`
keeps that GPU backend while `encoder_hw_accel` is `none` for libx265 encoding.
The same holds when a VAAPI device tone-maps a capped encode but offers neither
VBR nor CBR rate control; libx264 or libx265 then encodes.
Activity reporting uses `encoder_hw_accel`, falling back to `hw_accel` for
older nodes that omit it. Stored recipe cards preserve both values; execution
validates the encoder again when reconstructing a session.

## Subtitle renditions

A node that advertises the `hls_subtitle_rendition_v1` transport feature
accepts `subtitle_rendition: true` on `POST /transcode/start`, together with
`subtitle_track_index` (the `0:s:N` ordinal), `subtitle_codec`, and
`total_duration`. It emits that text track as WebVTT from the same FFmpeg
process and echoes `subtitle_rendition: true` in the start response. The API
rejects a missing echo and stops the transport, so an older node can never
serve a plan that promises a rendition. The API plans a rendition only when one
executor can run the whole recipe: itself, or a node that advertises both the
feature and every server transformation the plan needs. Otherwise the plan
keeps the sidecar. Recipe cards and signed reconstruction claims carry the
flag, so reconstruction and restarts keep it.

For such a session, `GET /transcode/{id}/master.m3u8` answers the multivariant
playlist, and the `silo_rendition` query parameter selects the video or
subtitle media playlist. `GET /transcode/{id}/segment/sub_NNNNN.vtt` answers a
WebVTT segment from the cues already on disk, waiting for FFmpeg to reach it
no longer than its video segment would. It is never acknowledged, pruned, or used to restart
FFmpeg. The protocol is described in
[playback protocol v3](architecture/playback-protocol-v3.md#8-track-identity-and-the-subtitle-ordinal-space).
