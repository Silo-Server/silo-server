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

`Request.Background` marks work nobody is waiting on. On Linux, a background
run's ffmpeg starts at nice 19 and in the idle I/O class, so it only gets CPU
and disk time that playback and the API leave free. Other platforms run it
like any other request. Intro detection sets `Background` for scheduled runs
and admin refreshes, and leaves it unset for analysis started from playback
(`intromarkers.WithPlaybackPriority`).

How it works: Linux keeps nice and I/O priority per thread, and a forked child
inherits them from the thread that forked it. The runner starts a background
ffmpeg from a goroutine locked to its own OS thread, lowers that thread's
priority, forks, and exits without unlocking, so the Go runtime discards the
thread. No other goroutine runs on it, and the runtime never creates new
threads from a locked one. The main thread is the exception: the runtime
parks it rather than discarding it, so a start that lands there hands the
work to another goroutine while it holds the main thread, and leaves the main
thread's priority unchanged.

Lowering priority needs no privileges. If it fails anyway (for example under
a seccomp profile that blocks `ioprio_set`), the run continues at normal
priority and the first failure is logged. Deadlines and the limiter still
bound background work. If the idle I/O class is seen to starve it on a disk
that is never idle, switch to the lowest best-effort level (7) instead.

## Artifact storage

Per-file analysis results are stored in `media_intro_fingerprints`, one row
per file and artifact. The table name predates generalization; renaming it
waits for a schema maintenance window. `intromarkers.Repository` reads and
writes it through `LoadArtifact`, `LoadArtifacts`, `UpsertArtifact`, and
`RecordArtifactFailure`; it moves to its own package when a second feature
stores artifacts.

- **Key.** The primary key is `(media_file_id, algorithm_version,
  config_hash)`. A row also has a `kind`, such as `intro_fingerprint`. Kinds
  never share a key because each derives its `config_hash` with
  `intromarkers.ArtifactConfigHash`, a hash of the kind and its parameters.
  Intro fingerprints keep `Config.ConfigHash`, which predates the namespacing
  and is pinned by a test. An upsert never takes over another kind's row.
- **Identity.** Each row records the file hash, size, duration, and analysis
  window it was computed from. A row applies only while all of them match the
  file.
- **Payload.** `points` holds the payload bytes and `point_count` the number
  of items in it; `fingerprint_format` names the encoding. The consuming kind
  owns the encoding.

Status rules, applied by `Artifact.State`:

| Status | Meaning | Next analysis |
|---|---|---|
| `complete` | The payload is valid. | Use it while the identity matches; otherwise compute again. |
| `unusable` | The file cannot yield this artifact; `detail` says why (for example `no_video` or `sparse`). | Skip while the identity matches. A changed file or config hash computes again. |
| `failed` | An error that may be transient, in `last_error`. | The server in `recorded_by` skips the file until `retry_after`. Other servers retry at once, since the cause may be local to that server. |

The retry delay starts at 12 hours and doubles for each consecutive failure on
the same server and unchanged file, up to 7 days. A failure recorded inside
the current delay (a forced run) does not extend it. A failure never replaces
a `complete` or `unusable` row for the same file identity. A later success
clears the failure.

`unusable` and `failed` rows carry an empty payload and, for intro
fingerprints, no Chromaprint format. Binaries that predate artifact statuses
ignore the status column and read such rows as cache misses, so the change
needs no maintenance window: those binaries keep inserting and upserting on
the same primary key and get the `intro_fingerprint` and `complete` defaults.
Their upserts do not reset `status`, so intro detection writes only `complete`
`intro_fingerprint` rows until no such binary can still be running.
