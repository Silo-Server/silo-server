# Web browser decode engine

The web player can decode an original file itself when the browser's media
element cannot: Dolby TrueHD, DTS (including DTS-HD MA), AC-3 and E-AC-3 audio,
HDR10, HLG and Dolby Vision base layers on SDR screens, Matroska in Firefox, and
a non-default audio track. The server then sends the untouched file
(`original_http`) instead of converting it. Paths are repository-relative.

The engine is a per-browser beta setting (Settings → Playback → This browser),
stored in `localStorage` under `silo-player-browser-decoding`, off by default.
With it off, nothing below loads and the player behaves as before.

## Capability contract

The engine adds to the existing `declared`-tier capability block
(`web/src/player/client-context-v3.ts`). It never changes the evidence tier.

- `original_http` gains the engine's containers (`mkv`, `mp4`), video codecs
  (whatever WebCodecs reports for H.264, HEVC, AV1, VP9, VP8), and audio codecs
  (WebCodecs AAC, Opus, MP3, FLAC; the bundled FFmpeg decoders for `ac3`,
  `eac3`, `dts`, `truehd`, `mlp`; uncompressed PCM).
- `original_http` gains the validated claims `client_selected_audio_track_v1`
  and, when the WebGPU presenter passes the probe and the display is SDR,
  `client_managed_dynamic_range_v1`
  (see [playback-protocol-v3.md](playback-protocol-v3.md) §3 and §4). The
  engine presents only SDR, so on an HDR display (`dynamic-range: high`) the
  claim stays off and HDR sources keep the routes that preserve HDR.
- The device-level `client_capabilities` lists carry the same additions,
  because the planner reads them before narrowing each route by its delivery
  class. `progressive` and `hls` keep the media element's own lists, so remux
  and transcode routes are unchanged.

The probe (`web/src/player/engine/capabilities.ts`) asks WebCodecs
`isConfigSupported` and checks the HDR presenter: a hardware (non-fallback)
WebGPU adapter, a device, both presenter pipelines compiled, and one picture
drawn through each path without a validation error (`probePresenter` in
`renderers.ts`). The codec answers and the HDR answer are each bounded by
`PROBE_TIMEOUT_MS`, and once a hardware adapter is found the rest of the HDR
check gets `HDR_CHECK_GRACE_MS`, because every playback start waits for the
probe and the check can take seconds on some Linux Vulkan drivers. A check
still running then counts as passed when nothing has failed, because the
presenter reports failures at runtime. What the check learns after its
deadline (its own answer, or a hardware adapter that answered late) applies
the next time the player opens. A device lost during the check, or a presenter chunk that
fails to load, leaves the answer open. Like the rest of the web block it is a declaration,
not a decode of real media. A source that passes the probe and still cannot be
decoded or presented fails with a typed classification, and the normal
`failure_recovery` replan excludes the plan's attempt key, so the server falls
through to a packaged route.

The server needs no change: the planner already honours both claims on
`original_http`.

## Routing an `original_http` plan

`web/src/player/engine/routing.ts` decides who plays the plan:

1. The media element, when it plays the container, video codec and audio codec
   natively, the source is SDR, and the selected audio track is the container
   default (the first track flagged default, else the first, as the server
   computes it). Hardware decode and the browser's own A/V sync win whenever
   they can.
2. The engine, when it covers the container, both codecs and the dynamic range.
   Dolby Vision plays its standards-compatible base layer: Profile 8.1 and
   Profile 7 as HDR10, 8.4 as HLG, 8.2 as SDR.
3. Otherwise neither, and the player reports
   `browser_engine_unsupported_source` before loading any bytes. Profile 5
   (no compatible base layer) and HDR without WebGPU land here.

## Runtime

```
VideoPlayer (page)
  <video>  — no source; keeps every listener, layout and ref
  EnginePlayer (engine/EnginePlayer.ts) — media-element behaviour, clock, rAF presentation
    ├─ engine.worker.ts — Mediabunny demux over HTTP ranges, WebCodecs video,
    │                     WebCodecs / FFmpeg WASM / PCM audio, downmix
    ├─ audio.worklet.ts — PCM ring and master clock, fed by the worker directly
    └─ renderers.ts     — 2D canvas (SDR), or for HDR a WebGPU tone map plus a 2D canvas layer
                          for frames only the browser can map
```

- **Facade.** `engine/facade.ts` installs instance-level overrides on the
  player's `<video>` (`currentTime`, `paused`, `readyState`, `play`,
  `requestVideoFrameCallback`, `addTextTrack`, …) that forward to the engine,
  which dispatches the media events on the element in the order a media
  element would. Deleting the overrides restores the native element. This is
  what lets every existing hook (progress, Watch Together, keyboard, subtitles,
  JASSUB) work unchanged. `addTextTrack` returns `EngineTextTrack`, because the
  browser only activates cues on a media element whose own timeline advances.
- **Clock.** With audio, the worklet reports which media time it rendered at
  which context time; the page reads that mapping at the context time reaching
  the speakers (`AudioContext.getOutputTimestamp`). Video frames are pulled
  against it on every animation frame, and late frames are released undrawn.
  Each report carries the rate the worklet rendered at, and the page reads
  from the newest report at or before the audible time, so a speed change
  takes effect when its audio reaches the speakers. Without audio, the clock
  runs from `performance.now()` and holds while the decoder is starved.
- **Credits.** The worker keeps at most 6 decoded frames outstanding and 1.5 s
  of audio ahead of what the worklet has played. While the tab is hidden,
  frames are presented on a 100 ms timer, so due frames are also released as
  they arrive; otherwise fast playback would outrun the credits. A seek starts
  a new generation; every message carries one, and stale ones are dropped
  wherever they land.
- **Audio.** The AudioContext opens at the track's sample rate. Surround
  passes through when `destination.maxChannelCount` allows it; otherwise the
  worker downmixes with ATSC A/52 Lo/Ro coefficients and the worklet soft-limits
  overs. At a playback rate other than 1 the worklet's read head still steps
  through the queue at that rate, which keeps the clock and the credits as at
  1x, but the samples come from a WSOLA time stretcher (`timeStretch.ts`) that
  keeps the pitch. Above 1x each sequence starts half its skip past the head,
  so the audio stays centred on the clock. It builds each 40–60 ms sequence
  only from PCM already queued (gaps in the timestamps count as silence), so
  running short is an underrun rather than silence played as audio. Watch
  Together still corrects the engine by seeking rather than by rate nudges.
- **Picture.** Each session draws on canvases it creates inside a box under the
  `<video>`: one for SDR, and for HDR a WebGPU canvas plus a 2D canvas added
  when needed, because a canvas keeps the first context type it hands out. The
  box is observed in device pixels (`device-pixel-content-box`) so canvases
  match the screen exactly; where the browser cannot report that (Safari), the
  box's CSS pixels times the device pixel ratio stand in, and a resolution
  media query redraws on a ratio change.
- **Readiness and autoplay.** The worker signals once enough audio has been sent
  to the worklet, because the worklet does not run while the AudioContext is
  suspended. `play()` refuses without events on a page that never had a user
  gesture, like a media element; a refused `AudioContext.resume()` settles the
  player paused. While playing, the engine holds a screen wake lock and
  answers the Media Session play and pause actions, which a playing `<video>`
  gets from the browser.

## HDR presentation

`engine/hdrShader.ts` mirrors the reference in `engine/colorMath.ts`: PQ or HLG
decode, BT.2390 EETF from a 1000-nit source peak to SDR white at 100 nits, the
source gamut to BT.709, sRGB encode. These match the server's tone-map recipes in
`internal/tonemap`, so a picture looks the same whichever side tone-maps it.

- **Planes.** Software-decoded 10/12-bit frames (`I420P10`, `I420P12` and their
  4:2:2/4:4:4 forms) are copied out with `VideoFrame.copyTo`, uploaded as
  `r16uint` textures and converted in the shader at full precision.
- **External textures.** The worker re-tags HDR decoder configurations as
  BT.709/sRGB so the browser applies only the Y'CbCr matrix when a GPU-backed
  frame is imported with `importExternalTexture`, leaving the PQ/HLG codes for
  the shader. Chromium imports these through an 8-bit surface, so this path
  quantizes before tone mapping. WebKit imports 10-bit hardware-decoded frames
  wrongly (the samples come out as if read at 8 bits), so each presenter draws
  a frame both ways, imported and copied with `copyExternalImageToTexture` into
  a half-float texture, at 8x8 and compares them. Where they agree it imports
  from then on; where they differ (WebKit's 10-bit frames) it copies every
  frame, at the cost of one GPU copy per frame. A dark or flat frame looks alike
  both ways even when the import is wrong, so such a check decides nothing and
  a later frame is tried. It copies until there is an answer, and imports where
  the browser cannot copy a VideoFrame (Firefox).
- **Routing.** Each picture goes to the path its kind and tag call for
  (`routePicture` in `engine/renderers.ts`):

  | Picture | Path |
  | --- | --- |
  | Uploaded planes | WebGPU, planes pipeline |
  | Frame tagged BT.709 primaries and sRGB transfer (the re-tag held) | WebGPU, external texture or a copy (see above) |
  | Frame tagged PQ or HLG (a hardware decoder ignored the re-tag: VideoToolbox, D3D11, VA-API) | 2D canvas, which tone-maps the way the browser's `<video>` does, when `canvasMapsHDR` |
  | Anything else | `browser_engine_presentation_failed` |

  Chromium's WebGPU import of a PQ/HLG-tagged frame is desaturated, so those
  frames go to the 2D canvas. `canvasMapsHDR` checks, once per page and
  transfer and only when a session first needs it, that a canvas set up like
  the presenter's maps a dark code, reference white and a highlight
  differently from sRGB and below clipping: Chromium and Safari do, Firefox
  draws the raw codes, so there such a frame fails over instead. The canvas's mapping is the browser's, not the
  server recipe's, so a session that switches paths changes brightness.
- **Source colour.** The worker reads the stream's primaries, matrix and chroma
  siting before the re-tag (`engine/pictureColor.ts`) and sends them with
  `ready`. Primaries other than BT.709 and P3 (D65), and matrices other than
  BT.709, are read as BT.2020, whether stated or unknown. Chroma siting comes from
  AV1's signalled `chroma_sample_position`; H.264 and HEVC use type 0, the
  position of an unsignalled stream (an explicit VUI value is not read); VP9,
  which signals none, and unsignalled AV1 keep the centred position. 4:2:2 is
  always co-sited horizontally.
- **Scaling down.** Each fragment averages an N x N grid of bilinear samples
  across its footprint, N being the texels of that plane the pixel covers (at
  most 4), so a picture drawn smaller than its size does not alias.
- **Errors.** Pipelines are built with `createRenderPipelineAsync` inside an
  error scope, and each draw runs inside a validation scope; the first error,
  or an `uncapturederror` event (allocation failures included), fails the engine with
  `browser_engine_presentation_failed` so the replan runs. WebGPU never throws
  for these, so without the scopes a broken presenter shows a black picture
  while audio plays.
- **Device.** The presenter uses the default adapter, the one the browser
  composites with, and treats a software adapter as none. The page keeps one
  presenter device: the probe offers its device as soon as the presenter
  compiles (its test draw can take seconds to report on some Linux drivers),
  and each playback hands it on when it ends unless it was lost or reported an
  error. The device subscribes to its loss and uncaptured errors once and
  forwards them to whichever renderer holds it; a renderer that takes a
  device handles what it reported before then, such as a loss during
  compilation, the same way. A lost device is replaced in
  place with a fresh one within `DEVICE_RECOVERY_TIMEOUT_MS`, at most
  `MAX_DEVICE_RECOVERIES` times per session. A hardware decoder dies with the
  GPU process too, so after a video decoder error `EnginePlayer` starts a new
  decode run at the current position, after `DECODER_RESTART_DELAY_MS` times
  the restarts so far and at most `MAX_DECODER_RESTARTS` times per session,
  showing only waiting and playing as a media element would; a run that then
  shows no picture, or a stream that keeps failing, fails over as a decode
  failure.
- **Layers.** Each session draws in its own box, which replaces whatever an
  earlier session left in the surface.
- **Dolby Vision.** RPU (NAL 62) and enhancement-layer (NAL 63 or
  `nuh_layer_id > 0`) units are removed before decode (`engine/hevc.ts`).

## Limits

- Containers are Matroska/WebM and MP4/MOV only; AVI and MPEG-TS stay on
  server routes.
- HEVC depends on the platform decoder WebCodecs exposes (hardware on Windows
  and macOS; usually absent on Linux). MPEG-2 and VC-1 are not decoded.
- Picture-in-picture is unavailable while the engine plays (the picture is a
  canvas); the player hides the control.
- Dolby Vision Profile 5 is refused; the server's own routes take it.

## FFmpeg audio decoders

`web/src/player/engine/wasm/silo-audio-decoders.{mjs,wasm}` are built from a
pinned FFmpeg release with only the `ac3`, `eac3`, `dca`, `truehd` and `mlp`
decoders enabled, plus `web/wasm/audio-decoders/bridge.c`. They are committed
so the web build never needs Emscripten. Rebuild them with
`web/wasm/audio-decoders/build.sh` (Docker) after changing the FFmpeg tag, the
decoder set, or the bridge; the FFmpeg commit is recorded in
`silo-audio-decoders.ffmpeg-revision`. FFmpeg is LGPL-2.1-or-later; see
`THIRD_PARTY_NOTICES.md`.
