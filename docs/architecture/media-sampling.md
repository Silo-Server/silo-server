# Media sampling

`internal/mediasample` owns every ffmpeg run that decodes a media file to
analyze it: audio fingerprints, silence, and later frame statistics and still
images. Intro detection (`internal/intromarkers`) runs all of its ffmpeg
processes through it.

## Scope

In scope: decodes whose output is data about the file, not a stream a client
plays. A new analysis feature adds an output type here instead of building its
own ffmpeg arguments, runner, or stderr parser.

Out of scope:

- Playback transcodes and remuxes (`internal/playback`).
- GPU-encode argument builders in `playback` and `tonemap`. They keep frames
  on the GPU for encoding, and `mediasample` imports `tonemap`, so `tonemap`
  cannot import it back.
- Media probing with ffprobe (`internal/scanner`).

## Requests

A caller describes a run as a `mediasample.Request` and a `Runner` executes
it. Rules:

- A request is plain data that round-trips through JSON. It never carries raw
  ffmpeg arguments or filter strings; the runner builds them. A remote node can
  therefore receive the same request and enforce its own input and hardware
  rules.
- A request names exactly one sampling mode and at least one output.
  `Validate` rejects anything else, and bounds the numbers (non-negative
  window start, positive duration, silence threshold at most 0 dB, thread and
  attempt counts).
- Result times are absolute media seconds. The runner adds the window start;
  callers never do window arithmetic.
- `Attempts` run in order until one succeeds. An empty list is one software
  attempt bounded only by the caller's context. A failed run returns an
  `*mediasample.Error` with a reason for each attempt (`canceled`, `timeout`,
  `start`, `exit`, `args`) and a bounded tail of ffmpeg's log. The error
  message quotes only ffmpeg's last log line, cleaned so it can be stored in a
  text column.
- Every run is recorded in the subprocess metrics under the runner's
  workload; intro detection uses `analysis`.

Supported today:

| Part | Values |
|---|---|
| Sampling mode | `Window` (start and duration; `KeyframesOnly` is reserved for video outputs) |
| Outputs | `Audio.Fingerprint` (raw Chromaprint points), `Audio.Silence` (silencedetect intervals) |
| Attempts | software only; hardware attempts need a video output |

Planned additions, each landing with its first consumer: frame statistics
(`Stats`), keyframe sampling at chosen times (`Samples`), an accurate single
frame (`At`), and still images (`Images`) with hardware decode.

## Argument stability

Intro fingerprints are cached per file for as long as the algorithm version
and config hash stay the same, and re-reading a large library's audio takes
days. Any change to the arguments of a fingerprint request must be shown to
produce byte-identical Chromaprint output on real ffmpeg (the production
jellyfin-ffmpeg build) before it lands; otherwise bump the fingerprint
`AlgorithmVersion` deliberately. The argument lists are pinned by tests in
both packages.

The runner uses input seeking (`-ss` before `-i`) and keeps `-t` as an output
option, in the order intro detection has always used.

## Capabilities

`LoadCapabilities` lists an ffmpeg binary's filters and muxers, and checks
that the chromaprint muxer can write raw fingerprints. `Capabilities.Require`
reports the first thing a request needs that the binary lacks.

- Inventories are cached per binary identity (resolved path, size, and
  modification time, the same identity `tonemap` uses), so replacing ffmpeg in
  place loads a new inventory.
- Concurrent loads share one set of listing commands, each bounded at three
  seconds and independent of any one caller's context. Failures are not
  cached.
- The node capability re-probe calls `InvalidateCapabilities` beside
  `tonemap.InvalidateProbeCache`.

## Concurrency

`mediasample.Limiter` bounds how many runs a consumer starts at once. Its
capacity can change while slots are held: after a decrease, running work
finishes before new work starts. One extra slot is reserved for work a viewer
is waiting on, marked with `WithInteractive`, so it waits behind at most one
background run. Intro detection sizes its limiter from
`markers.detection_workers`.

## Process priority

`Request.Background` marks work nobody is waiting on. It does not change how
the process runs yet.

## Artifact storage

Analysis results are stored by their consumers today; intro detection keeps
fingerprints in `media_intro_fingerprints`.
