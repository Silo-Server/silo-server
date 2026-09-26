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
nodes that omit this field. Deploy updated transcode nodes before enabling
throttling on API servers that require this attestation.

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
