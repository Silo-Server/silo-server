# Media sampling

`internal/mediasample` owns every ffmpeg run that decodes a media file to
analyze it: audio fingerprints, silence, frame statistics, and later still
images. Intro and credits detection (`internal/intromarkers`) run all of
their ffmpeg processes through it.

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
  window start, positive duration, sample times, silence threshold at most
  0 dB, thread and attempt counts).
- Result times are absolute media seconds. The runner adds the window start;
  callers never do window arithmetic.
- `Attempts` run in order until one succeeds. An empty list is one software
  attempt bounded only by the caller's context. A failed run returns an
  `*mediasample.Error` with a reason for each attempt (`canceled`, `timeout`,
  `start`, `exit`, `args`) and a bounded tail of ffmpeg's log. The error
  message quotes only ffmpeg's last log line, cleaned so it can be stored in a
  text column.
- `Classify` names a failed run's cause from its last attempt: `canceled`,
  `timeout`, `killed` (a signal ffmpeg did not ask for), `no_stream` (an
  output's stream is missing), `unsupported` (this ffmpeg lacks a filter,
  option, muxer, or decoder), `invalid_data` (the input cannot be demuxed or
  decoded), or `failed`. `Reason.Permanent` is true only for `invalid_data`
  and `no_stream`, which the file itself causes; a caller may record those as
  unusable and back off on the rest.
- Every run is recorded in the subprocess metrics under the runner's
  workload; intro detection uses `analysis`.

Supported today:

| Part | Values |
|---|---|
| Sampling mode | `Window` (start and duration; `KeyframesOnly` decodes only video keyframes and needs `Stats`), `Samples` (the keyframe at or before each of a list of times; `Stats` only) |
| Outputs | `Audio.Fingerprint` (raw Chromaprint points), `Audio.Silence` (silencedetect intervals), `Stats` (per-frame picture statistics) |
| Attempts | software only |

Audio and `Stats` may share one run: ffmpeg reads the input once and writes
the audio output first and the statistics of the first video stream
(`-map 0:V:0`) second, each with its own `-t`.

`Stats` crops each frame to its centered `CropWidth` by `CropHeight` share,
scales it to `Width` pixels wide, converts it to 8-bit 4:2:0, and measures it
with one `blackframe` per entry of `BlackThresholds` and with `signalstats`.
`metadata=print` logs each frame's time and values. The runner joins them by
frame index: both filters count the frames of one linear chain, and each
filter instance is told apart by its position in the chain
(`Parsed_blackframe_3`). A frame reports its absolute time, the share of
pixels darker than each threshold (`PBlack`), and luma and saturation
minimum, 10th percentile, average, 90th percentile, and maximum. A frame
missing any value, such as one cut short when ffmpeg stopped, is dropped.

`Samples` reads only the stretches of the file it samples. The runner first
opens the input on its own (`-t 0` stream copy) and reads the container and
its start time from the input header ffmpeg logs. It then writes an ffconcat
list to ffmpeg's stdin that names the input once per sample time: an
`inpoint` at the time plus the container's start time, an `outpoint` 40 ms
later, and a `file_packet_meta sample <time>` tag. With `-skip_frame:v nokey`
the concat demuxer seeks to the keyframe at or before each inpoint and ffmpeg
decodes only that keyframe; `metadata=print` logs the tag with the frame's
statistics. Rules:

- A frame reports the time it was sampled for, not its own, which is up to
  one keyframe interval earlier. Frame timestamps follow the list, not the
  input, so a frame without the tag is dropped. A keyframe that serves
  several sample times is decoded once for each; when one sample decodes a
  second keyframe, the first is kept.
- Sample times are finite, non-negative, strictly increasing, and at most
  10,000 per request. `Samples` takes no audio output.
- The list is read from `pipe:0`, so each entry names the input as an
  explicit `file:` URL, and the input opens with
  `-protocol_whitelist file,pipe`; the concat demuxer otherwise refuses both.
  Paths are single-quoted, with a quote written as `'\''`; a path with a
  line break or NUL is rejected, since the list is read line by line.
- Inpoints are container timestamps, while sample times count from the
  start of the file like every other result time, hence the start-time
  offset. Files remuxed with their original timestamps (`copyts`) often
  start well above 0.
- Only Matroska/WebM, MP4/MOV, and AVI, whose indexes let the demuxer seek
  to a keyframe, are read through the list. Other containers, such as
  MPEG-TS and M2TS, seek by timestamp to a packet that is rarely a keyframe,
  so a list would decode nothing. They are read as one `KeyframesOnly`
  window from 10 s before the first sample to the last, and each sample
  takes the last keyframe at or before its time: the same frames, at the
  cost of reading the whole span.
- The VP9 decoder ignores `-skip_frame nokey`, so a VP9 sample decodes every
  frame from its keyframe to the outpoint. The first frame is still the one
  kept, but the cost grows with the keyframe interval.
- `Capabilities` offers `Samples` only when ffmpeg reads a one-sample list
  naming a missing file as far as opening that file. An ffmpeg without the
  concat demuxer, a list directive, or a protocol fails on the list itself
  with "Invalid data found when processing input", which would otherwise
  read as a broken file. This works with FFmpeg 7.1 (jellyfin-ffmpeg 7.1.4)
  and later.

Outputs read from ffmpeg's log run at `-loglevel repeat+info`: without
`repeat`, ffmpeg folds identical consecutive lines into "Last message
repeated N times" and per-frame values would be lost. Fingerprint-only runs
keep `-loglevel warning`.

Planned additions, each landing with its first consumer: an accurate single
frame (`At`) and still images (`Images`) with hardware decode.

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
reports the first thing a request needs that the binary lacks: the
chromaprint muxer for a fingerprint, `silencedetect` for silence, and
`blackframe`, `signalstats`, and `metadata` for `Stats`.

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
| `unusable` | The file cannot yield this artifact; `detail` says why (for example `no_stream` or `sparse`). | Skip while the identity matches. A changed file or config hash computes again. |
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
