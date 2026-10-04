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
  and, when WebGPU is available, `client_managed_dynamic_range_v1`
  (see [playback-protocol-v3.md](playback-protocol-v3.md) §3 and §4).
- The device-level `client_capabilities` lists carry the same additions,
  because the planner reads them before narrowing each route by its delivery
  class. `progressive` and `hls` keep the media element's own lists, so remux
  and transcode routes are unchanged.

The probe (`web/src/player/engine/capabilities.ts`) asks WebCodecs
`isConfigSupported` and requests a WebGPU adapter. Like the rest of the web
block it is a declaration, not a decode of real media. A source that passes
the probe and still cannot be decoded fails with a typed classification, and
the normal `failure_recovery` replan excludes the plan's attempt key, so the
server falls through to a packaged route.

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
    └─ renderers.ts     — 2D canvas (SDR, or HDR the browser tone-maps) or WebGPU tone mapping (HDR)
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
  Without audio, the clock runs from `performance.now()` and holds while the
  decoder is starved.
- **Credits.** The worker keeps at most 6 decoded frames outstanding and 1.5 s
  of audio ahead of what the worklet has played. A seek starts a new
  generation; every message carries one, and stale ones are dropped wherever
  they land.
- **Audio.** The AudioContext opens at the track's sample rate. Surround
  passes through when `destination.maxChannelCount` allows it; otherwise the
  worker downmixes with ATSC A/52 Lo/Ro coefficients and the worklet soft-limits
  overs. A playback rate other than 1 (Watch Together catch-up) resamples, so
  pitch shifts.
- **Picture.** Each session draws on a fresh canvas inside a box under the
  `<video>`, because a canvas keeps the first context type it hands out.
- **Readiness and autoplay.** The worker signals once enough audio has been sent
  to the worklet, because the worklet does not run while the AudioContext is
  suspended. `play()` refuses without events on a page that never had a user
  gesture, like a media element; a refused `AudioContext.resume()` settles the
  player paused. While playing, the engine holds a screen wake lock and
  answers the Media Session play and pause actions, which a playing `<video>`
  gets from the browser.

## HDR presentation

`engine/hdrShader.ts` mirrors the reference in `engine/colorMath.ts`: PQ or HLG
decode, BT.2390 EETF from a 1000-nit source peak to SDR white at 100 nits,
BT.2020 to BT.709, sRGB encode. These match the server's tone-map recipes in
`internal/tonemap`, so a picture looks the same whichever side tone-maps it.

- **Planes.** Software-decoded 10/12-bit frames (`I420P10`, `I420P12` and their
  4:2:2/4:4:4 forms) are copied out with `VideoFrame.copyTo`, uploaded as
  `r16uint` textures and converted in the shader at full precision.
- **External textures.** GPU-backed frames are imported with
  `importExternalTexture`. The decoder is configured with an sRGB/BT.709 color
  space tag so the browser applies only the YUV matrix and leaves the PQ/HLG
  encoding for the shader. Chromium imports these through an 8-bit surface, so
  this path quantizes before tone mapping.
- **Browser tone mapping.** Hardware decoders (VideoToolbox on macOS, and
  likely D3D11 and VA-API) ignore that tag: their frames still report a PQ or
  HLG `colorSpace.transfer`, and Chromium's external-texture import of them
  is desaturated. On the first such frame the engine replaces the WebGPU canvas
  with a 2D canvas and draws frames with `drawImage`, which tone-maps the way
  the browser's own `<video>` does (`browserMapsToSDR` in
  `engine/renderers.ts`).
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
