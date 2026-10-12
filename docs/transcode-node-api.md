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

The start response also reports `hw_accel`, the backend retained for video
decoding and tone mapping, and optional `encoder_hw_accel`, the actual video
encoder backend. When a GPU can tone-map but cannot encode HEVC, `hw_accel`
keeps that GPU backend while `encoder_hw_accel` is `none` for libx265 encoding.
The same holds when a VAAPI device tone-maps a capped encode but offers neither
VBR nor CBR rate control; libx264 or libx265 then encodes.
Activity reporting uses `encoder_hw_accel`, falling back to `hw_accel` for
older nodes that omit it. Stored recipe cards preserve both values; execution
validates the encoder again when reconstructing a session.

## Intel QSV runtime packaging

The Dockerfile's `INTEL_QSV_RUNTIME` build argument selects the packaged Intel
runtime. `auto` is the unchanged default. An explicit amd64 `vpl` image removes
only Jellyfin's `libmfxhw64.so*` legacy Media SDK runtime while retaining the
modern VPL GPU runtime, dispatcher, iHD driver and dependencies. Invalid options,
non-amd64 VPL selection, or a missing modern runtime fail the build. The image's
`io.silo.intel-qsv-runtime` label records the selection.

The VPL option addresses a measured mixed-library selection failure on an
isolated native non-first Flex render device: the legacy runtime selected there
fails QSV device initialization, while removing it selects the modern runtime.
The dispatcher ordering defect is not established. Both capability probing and
actual transcodes use the same packaged FFmpeg and allocated native render path;
this option never renames devices or grants additional GPUs.

Legacy Intel support remains in the default image. Intel's
[dispatcher hardware matrix](https://github.com/intel/libvpl#dispatcher-behavior-when-targeting-intel-gpus)
lists older Media SDK-only platforms and Flex/DG2 as VPL platforms, so removing
Media SDK globally would narrow support. Use the explicit VPL image for modern
hardware; validate the resulting image on assigned native render devices. The
packaging regression is `sh scripts/tests/intel-qsv-runtime.sh`; actual H.264,
HEVC, VA-API and device-isolation acceptance requires Intel hardware.
