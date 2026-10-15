# Media sampling

`internal/mediasample` owns every ffmpeg run that decodes a media file to
analyze it or take a still image from it: audio fingerprints, silence, frame
statistics, and single-frame images. Intro and credits detection
(`internal/intromarkers`) run all of their ffmpeg processes through it, and
chapter thumbnails (`internal/chapterthumbs`) extract their frames with it.

## Scope

In scope: decodes whose output is data about the file or a still image, not a
stream a client plays. A new analysis feature adds an output type here
instead of building its own ffmpeg arguments, runner, or stderr parser.

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
  attempt bounded only by the caller's context. The run stops early when the
  caller's context ends or when `Runner.Fallback`, given the failed attempt,
  declines to move on; without a `Fallback` every attempt runs. A failed run
  returns an `*mediasample.Error` with a reason for each attempt (`canceled`,
  `timeout`, `start`, `exit`, `args`; for images also `empty`, `unsupported`,
  and `capabilities`, see [Images](#images); for sheets also `empty` and
  `output`, see [Sheets](#sheets)) and a bounded tail of ffmpeg's log.
  The error message quotes only ffmpeg's last log line, cleaned so it can be
  stored in a text column.
- `Classify` names a failed run's cause from its last attempt
  (`AttemptError.Cause` does so for any one attempt): `canceled`, `timeout`,
  `killed` (a signal ffmpeg did not ask for), `no_stream` (an output's stream
  is missing), `unsupported` (this ffmpeg or host lacks a filter, option,
  muxer, decoder, or render device),
  `capabilities` (the capability listing a request needs failed),
  `invalid_data` (the input cannot be demuxed or decoded), or `failed`.
  `Reason.Permanent` is true only for `invalid_data` and `no_stream`, which
  the file itself causes; a caller may record those as unusable and back off
  on the rest.
- Every run is recorded in the subprocess metrics under the runner's
  workload; intro detection uses `analysis` and chapter thumbnails
  `thumbnail`. A `Runner.Exec` replacement, which tests set, starts no
  process and records nothing.

Supported today:

| Part | Values |
|---|---|
| Sampling mode | `Window` (start and duration; `KeyframesOnly` decodes only video keyframes and needs `Stats`), `Samples` (the keyframe at or before each of a list of times; `Stats` or `Sheets`), `At` (the frame at a time; `Images` only) |
| Outputs | `Audio.Fingerprint` (raw Chromaprint points), `Audio.Silence` (silencedetect intervals), `Audio.Speech` (speech-band level every 10 ms), `Stats` (per-frame picture statistics), `Images` (JPEG images), `Sheets` (JPEG sprite sheets of the samples) |
| Attempts | software; hardware (QSV, VAAPI, VideoToolbox) for requests with a video output (`Images`, `Stats`, or `Sheets`); see [Hardware decode](#hardware-decode) |

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
  second keyframe, the first is kept. Sheets lists over Matroska start their
  entries at keyframes instead (see [Keyframe entries](#keyframe-entries)).
- Sample times are finite, non-negative, strictly increasing, and at most
  10,000 per request. `Samples` takes no audio output.
- `Samples.ReadThrough` reads the sampled span as the one keyframes-only
  window below, whatever the container, instead of seeking to each sample.
  Each list entry opens and probes the input again and reads from the
  keyframe before its inpoint, so when samples lie closer together than the
  keyframes do, one pass front to back can cost less.
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
  cost of reading the whole span. `Sheets` also fall back to that window
  when the HEVC decoder drops listed keyframes as duplicates (see the
  `Sheets` rules below).
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

`Audio.Speech` takes a `Window` and no other output. It maps one audio
stream (`AudioStream`, ffmpeg's `0:a:N`), optionally only its front-centre
channel (`CenterChannel`, which fails on a stream without one), band-limits it
to 200-3400 Hz, and resamples it to 8 kHz mono with
`aresample=async=1:first_pts=0`, so a stream that starts after the window
start is padded with silence and level `i` always covers window start plus
`i` × 10 ms. ffmpeg writes raw samples to stdout and the runner reduces them
to one level per frame as they arrive (whole dB above -100 dBFS), so no window
is held as PCM. Subtitle sync is its consumer.

## Remote runs

`POST /media-samples/run` on the transcode-node listener runs one `Request`
with the node's ffmpeg and returns its `Result` as JSON
(`mediasample.RemoteClient` is the caller). The node requires a
[signed request](worker-http-protocol.md#authentication) and an input path it is allowed to read, accepts software attempts
only, and answers a failed run with `422` and a `RemoteFailure` whose reason is
the run's `Classify` cause. `RemoteError.Infrastructure` tells a caller
whether running the request elsewhere may succeed: an unreachable node, a 5xx,
or a node lacking a capability, but not a file that has no stream or cannot be
decoded. Callers choose and reserve nodes themselves
(`nodepool.Reservations`); the node also admits at most
`subtitles.sync_node_capacity` runs at once across every caller; a request
over the limit waits up to two minutes for a slot (`MaxRemoteAdmissionWait`)
and is then refused with `503` and `node_unavailable`.

## Hardware decode

An attempt with `Hardware` set decodes the request's video on the runner's
`HWAccel` (`hwdecode.go`). `HWAccel` is a backend the caller resolved with
`playback.ResolveHWAccelWithFFmpegContext` from `playback.hw_accel` and
`playback.hw_device`, never `auto`. A consumer that samples repeatedly keeps a
`HardwareResolver` (`hwresolver.go`), which caches the resolved backend per
configured pair until the playback probe cache is invalidated, and asks again
after `HardwareRetryInterval` when `auto` resolved to no hardware; credits
detection uses one. QSV, VAAPI, and VideoToolbox decode
every video output (`SupportsHardwareDecode`); NVENC decodes only `Sheets`
(`SupportsHardwareSheets`), on CUDA, so the stats chains cached intro
fingerprints come from and the chapter-thumbnail images stay as they were. A
hardware attempt needs a video output (`Images`, `Stats`, or `Sheets`); one
whose output its accelerator does not decode fails as `unsupported` without
starting ffmpeg. Audio in the same run always decodes in software.

- QSV and VAAPI decode into VAAPI surfaces on a render device. QSV
  initializes its VAAPI parent device the way playback does
  (`tonemap.QSVInitDeviceArgs`) and decodes through it.
- NVENC decodes into CUDA surfaces on the CUDA device `HWDevice` names (an
  index, or the default when empty), declared as the filters' device too:
  `-init_hw_device cuda=cu[:N] -filter_hw_device cu -hwaccel cuda
  -hwaccel_output_format cuda`.
- VideoToolbox decodes `Images` into system-memory frames, so software
  filters apply to them directly. `Stats` asks for VideoToolbox surfaces
  (`-hwaccel_output_format videotoolbox_vld`) and downloads them, because
  only surfaces prove the decode ran on hardware: given a stream VideoToolbox
  cannot decode, such as VP8, plain `-hwaccel videotoolbox` quietly decodes
  in software, while the download of a software frame fails the attempt.
- Each hardware attempt reserves one device through
  `playback.AcquireHWDevice`, falling back to `playback.PickRenderDevice` for
  QSV and VAAPI, before its timeout starts, and releases it when the attempt ends, before
  the next attempt. A multi-device `Runner.HWDevice` list therefore resolves
  to one device per attempt. A hardware attempt that cannot be built, such as
  VAAPI without a render device, fails as `unsupported` without starting
  ffmpeg.
- A caller that wants a result on any host lists a software attempt after
  the hardware one. A failed run takes its reason from its last attempt, so
  once the software attempt has run, a hardware failure costs only time and
  cannot mark an artifact unusable.
- A GPU that cannot decode the stream makes ffmpeg log "Failed setup for
  format …" or "doesn't support hardware accelerated …" and fall back to
  software frames, which the GPU chain refuses; the attempt's cause is
  `unsupported`.

`Stats` on VAAPI surfaces (VAAPI and QSV) scales the whole picture on the
GPU and converts it to 8-bit NV12 there before the download, since a 10-bit
source decodes into P010 surfaces that not every driver downloads as NV12.
The crop follows:

```text
scale_vaapi=w=SW:h=-2:format=nv12,hwdownload,format=nv12,crop=…,format=yuv420p,blackframe=…,signalstats,metadata=print
```

`SW` is `Width / CropWidth` rounded to an even number (534 for the credits
pass's 0.9 crop to 480), so the cropped picture is about `Width` pixels wide,
as in software. VideoToolbox surfaces are downloaded as they are, then take
the software chain:

```text
hwdownload,format=nv12,crop=…,scale=W:-2:flags=area,format=yuv420p,blackframe=…,signalstats,metadata=print
```

`hwdownload` cannot convert, and a VideoToolbox surface keeps its source's
depth, so the download names `p010le` when the request's `VideoBitDepth` is
above 8. Callers pass a probed depth through `VideoBitDepthHint`, which
turns anything outside 1..16 into unknown so the request stays valid for its
software attempt. An unknown depth is taken as 8; a 10-bit source without its depth,
or a 4:2:2 one, fails the hardware attempt. `-skip_frame:v nokey` still applies, in `Window` and `Samples` modes
alike. The VAAPI chain measures slightly different pixels (GPU scaling
before the crop instead of area scaling after it). On a 4K HEVC Dolby Vision
episode, a 1080p H.264 episode, and a 1080p movie, its credits keyframe
classes agreed with software on 99.8 to 100 percent of keyframes, with the
same runs and the same credits; VideoToolbox classified synthesized H.264
and 10-bit HEVC tails identically to software. Statistics are therefore
comparable across decoders, and artifacts do not record which one produced
them. `Result.Decoder` names the attempt that produced a result
(`hardware:vaapi`, `software`).

## Images

`At` with an `Images` output decodes one frame and returns it as a JPEG
(`Result.Images`, one `Image` with the requested time). The arguments keep
the layout chapter thumbnails have always used: `-loglevel error`, the
hardware decode options, an accurate input seek (`-ss` with three decimals
before `-i`, so ffmpeg decodes from the previous keyframe up to the time),
one frame (`-frames:v 1`), and the MJPEG encoder writing to stdout. ffmpeg's
whole stdout is the image; a run that succeeds without writing one, such as
a time past the end of the video, fails the attempt as `empty`.
`ImageOutput.Width` scales the image to an even width, keeping the aspect
ratio; zero keeps the source size.

A hardware image attempt downloads VAAPI surfaces as NV12
(`hwdownload,format=nv12`) before any scaling; see
[Hardware decode](#hardware-decode).

`ImageOutput.ToneMap` converts an HDR source to SDR with the tone-map chains
chapter thumbnails have always used, kept byte for byte; they differ from the
playback chains in `tonemap`. VAAPI and QSV tone map on the GPU
(`procamp_vaapi` and `tonemap_vaapi` before the download). Software attempts
and VideoToolbox attempts tone map in software, which only happens with
`ToneMap.AllowSoftware`; otherwise the attempt fails as `unsupported` before
ffmpeg starts. The software chain is `tonemapx` (BT.2390) when the binary
lists it and the standard `tonemap` filter's Hable curve otherwise, and both
need `zscale`. The runner loads the capabilities for that choice once per
run, when the first attempt that needs them starts, so a hardware attempt
that succeeds never loads them. A failed load fails the attempt as
`capabilities`, a missing filter as `unsupported`.

Chapter thumbnails keep their own attempt plan and failure reasons on top of
this: a hardware-capable backend tries hardware and then software (unless
software tone mapping is needed but not allowed), another configured backend
such as NVENC tries an SDR frame twice in software, and no backend tries once
in software. `chapterthumbs` maps each failed attempt to the reasons it
persists (`decode_invalid_data`, `tonemap_unsupported`, `ffmpeg_probe_failed`,
`hw_killed`, `hw_timeout`, `cpu_timeout`, `chapter_extract_failed`) with its
own log rules rather than `Classify`, because those reasons decide per-file
backoff. Its `Runner.Fallback` ends the run only after a hardware attempt
that found invalid data or whose VideoToolbox software tone mapping was
refused; every other failure moves on to the next planned attempt.

## Sheets

A `Samples` request with a `Sheets` output tiles the sampled frames into
JPEG sprite sheets, the images seek-bar previews are cut from. Each sample
fills one cell of a `Columns` by `Rows` grid, left to right and top to
bottom; every sheet keeps the full grid size, and the cells after the last
sample are black, because Jellyfin clients cut cells from an assumed full
grid.

The chain scales each frame to exactly `TileWidth` by `TileHeight` (the
caller derives the height from the display aspect ratio), then converts it
to full-range BT.601 4:2:0, the colors JFIF decoders read, and writes it raw
to stdout:

```text
scale=W:H:flags=area:out_range=full:out_color_matrix=bt601,format=yuv420p,
metadata=mode=add:key=sample:value=-1,metadata@sheets=print
-fps_mode passthrough -pix_fmt yuv420p -f rawvideo pipe:1
```

`Sheets.UseInputAspect` derives the height from the input header instead,
including a 90-degree display matrix, and returns the actual cell height in
`Result.SheetTileHeight`. This reuses the sampling probe, which every
`Sheets` request runs, `ReadThrough` included. Callers publishing manifests
must use the returned height and reject chunks whose geometry differs.

- GPU surfaces (VAAPI, QSV, CUDA) are scaled on the GPU to four tiles' size,
  or the frame's own size if smaller, downloaded as NV12 (P010 for a source
  tone mapped below), and area-scaled to the tile in software:

  ```text
  scale_vaapi=w=min(iw\,4W):h=min(ih\,4H):format=nv12,hwdownload,format=nv12,scale=W:H:flags=area:out_range=full:…
  scale_cuda=w=min(iw\,4W):h=min(ih\,4H):interp_algo=bilinear:format=nv12,hwdownload,format=nv12,scale=W:H:flags=area:out_range=full:…
  ```

  The GPU scalers sample a few source pixels per output pixel, so scaling a
  4K frame straight to a 300-pixel tile aliases: SSIM 0.94 against the
  software area scale on four 4K frames, where the two-step scale gives 0.999
  and downloads a fraction of the frame.
- HDR sources are tone mapped in software after scaling, so the tone map
  only handles thumbnail-sized frames, followed by
  `scale=out_range=full:out_color_matrix=bt601`.
- `ToneMap.Source` names the base signal `tonemap.ResolveSource` classified
  the input as. With it, every attempt converts with the chain playback
  converts that signal with in software (`tonemap.SoftwareFilter`) on a
  filter the binary lists: `tonemapx`, else `tonemap`'s Hable curve, which
  cannot apply a Dolby Vision RPU. GPU attempts decode and scale as above,
  download 10-bit P010 (`format=p010`, `hwdownload,format=p010le`), and take
  the same software chain, so every decoder gives the same colors. The GPU
  tone mappers do not: on an Intel Arc, `tonemap_vaapi` crushed PQ midtones
  and turned HLG teal and inverted, AMD's refuses HDR, and neither applies an
  RPU. A frame keeps its Dolby Vision RPU through the GPU scale and the
  download (SSIM 0.986 against the software render, 0.85 without the RPU).
  An attempt the binary has no filter for, such as a Dolby Vision base
  (Profile 5, compatibility id 0) without `tonemapx`, or one that does not
  allow software tone mapping, fails as `unsupported` before ffmpeg starts,
  so it makes no sheets rather than green and magenta ones. A base that is
  already SDR only changes gamut. Without `Source` the [Images](#images)
  chains apply, as best effort for sources whose catalog facts cannot be
  classified; CUDA has none of those, so its attempt is `unsupported`. Images
  take no `Source`.
- `metadata=print` logs a frame only when it carries metadata, so
  `metadata=mode=add` gives every frame a `sample` entry of -1 unless its
  packet was tagged. The log then counts every frame on stdout, and the Nth
  raw frame is the frame logged as `frame:N`. A filter graph reinit, which a
  hardware decoder that renews its surfaces causes mid-list, restarts the
  counter at zero after the old graph passed on every frame it logged; the
  frames still line up, and the count goes on. A counter that skips or
  restarts anywhere else, frames without log lines, log lines without frames,
  or bytes after the last whole frame fail the attempt as `output`.
- `-fps_mode passthrough` is required: a list's timestamps jump back at every
  entry, and rawvideo would otherwise pick a constant frame rate and drop or
  duplicate frames. Windows bound their duration with an input `-t`, since an
  output `-t` drops frames after the filters logged them.
- ffmpeg logs a frame before it writes it, but stdout and stderr are read by
  separate goroutines. The assembler places a frame once both halves are in
  and never makes either reader wait for the other.
- A sampled list places each frame by its tag; when one sample decodes two
  frames (a later keyframe within its span, or an all-intra source), the
  first, which is at or before the sample time, is kept.
- Sheets list entries last half a second rather than 40 ms. With B-frames,
  the packet after a keyframe in decode order can carry a later presentation
  time that ends a 40 ms entry before the decoder releases the keyframe, and
  the sample decodes nothing: 20 of 656 samples of one MP4 fixture did, and
  half a second recovered all of them at no measurable cost. Stats keep the
  40 ms span their cached analyses were computed with. A window gives each
  sample the last frame at or before its time.
- Sheets decode keyframes as the probed video codec needs. Interlaced MPEG-2
  often codes a keyframe as an I field paired with a P field, which the
  decoder reports as a P frame and `nokey` discards: a DVD-sourced episode
  decoded 2 of 80 samples. MPEG-2 therefore decodes with
  `-skip_frame:v noref`. The MPEG-1/2 decoder honors only `noref` and
  `nokey` (`nointra` acts as `noref`), so this decodes every I and P
  picture, about two and a half times the work of `nokey`. A list still
  keeps each sample's first frame, its keyframe; a window gives each sample
  the last I or P picture at or before its time. MPEG-1 has no field
  pictures and keeps `nokey`.
- MPEG-4 Part 2 (`mpeg4`, `msmpeg4*`) adds `-flags:v +low_delay`. Its
  decoder otherwise holds each picture back for B-frames that a
  keyframes-only read never sends, and releases it when the next keyframe
  arrives; in AVI, whose packets carry no PTS, the frame then takes that
  keyframe's time, and every cell showed the keyframe a GOP before its own.
  Keyframes-only and reference-only reads decode no B-frames, so low delay
  changes no picture. The repeat repair keeps its own `noref`.
- A window also copies the same input's video packets to a `framecrc`
  timing file in a private temporary directory, separate from stderr and
  removed when the attempt ends. Their presentation times and durations
  bound the final keyframe's coverage without decoding extra frames or
  reading the file again. Samples beyond that observed extent remain missing, so
  premature EOF cannot turn every trailing cell into a decoded preview.
- A packet without a PTS counts by its DTS only when the decoder runs in
  low delay (MPEG-4 Part 2, above). A DTS never runs ahead of its PTS, so
  the extent can only shrink, by the reorder delay. Without a PTS the decoder
  stamps each frame with the DTS of the packet that released it, which is
  the frame's own only when the decoder releases each keyframe as it
  arrives. A reordering decoder does not: H.264 with B-frames in AVI
  releases each keyframe two GOPs late in a keyframes-only read, and low
  delay does not change that. Such windows fail with no usable packet timing
  rather than publish every cell at an earlier keyframe.
- A cell without a frame shows the previous cell's frame, and cells before
  the first frame show the first, so the grid never shifts. A run that had
  to fill more than 10 % of its cells (and more than one) fails as `empty`,
  which sends a hardware attempt on to software.
- A list run that fails as `empty` after the decoder logged a dropped
  keyframe is read again, in the same attempt, as a window. Two messages
  mark a dropped keyframe. One decoder serves every list entry and nothing
  resets it between them, so an HEVC CRA keyframe (open GOP) after a jump
  takes a picture order count derived from the previous sample's. When that
  count matches a picture still in the decoder's buffer, the decoder drops
  the keyframe ("Duplicate POC in a sequence", then "Skipping invalid
  undecodable NALU: 21"). An H.264 keyframe whose reference marking names
  pictures a keyframes-only read skipped is discarded too ("illegal short
  term buffer state"). Either way such a file can lose nearly every sample
  on every run. An HEVC list goes to the keyframes-only window that other
  containers use, which decodes keyframes in order and keeps the counts
  consistent, at the cost of reading the whole span. An H.264 list that
  logged its message goes straight to the reference-picture window below,
  since a keyframes-only window discards the same keyframes. A list that is
  empty without either message, such as a truncated or damaged file's,
  still fails without the extra read: damaged pictures log only "Skipping
  invalid undecodable NALU", and a window would give their samples the last
  good keyframe instead of failing.
- Skipped pictures do not advance the decoder's count, so keyframes a whole
  count cycle apart (256 pictures with the common 8-bit count) collide in a
  keyframes-only window too, and H.264 windows discard keyframes as lists
  do. A window that logged a dropped keyframe finds, for each sample that
  holds a frame, the last keyframe packet at or before it in the timing
  file; a sample whose own keyframe was not decoded shows an earlier one and
  counts as filled. Such a window may fill up to 15 % of its cells instead
  of 10 %, since the file is whole and a preview a keyframe early is still
  usable; a list keeps 10 %, so too many drops still send it to the window.
  A window that fills more, or that logged a drop and has a keyframe packet
  without a time, which leaves the stale samples unknown, is read once more
  with `-skip_frame:v noref` and `select=key` ahead of the sheets chain:
  the decoder sees every reference picture, so its state stays whole, and
  the chain still gets only the keyframes. That read costs several
  keyframes-only reads and runs only for files that fail without it. If the
  window fails for any reason, the attempt reports the window's failure,
  whose reason and log describe the latest read, and after a list fallback
  its error names the list run's.
- Sheets are JPEG-encoded in-process (`image/jpeg`, `Quality`) once the run
  and its repairs are done, since a repair may replace any cell. A run holds
  its tiles until then, so a request whose sheets exceed 64 million tile
  pixels is invalid; callers chunk longer files. `Result.Sheets` holds them
  in order and `Result.SheetFrames` counts the decoded, filled, and
  repaired (`Refined`) cells.

### Keyframe entries

A sample-time entry makes the concat demuxer seek to the keyframe before the
sample and read every packet of every stream, video and all audio tracks,
from there up to the sample; the decoder skips them. With keyframes several
seconds apart that is most of the file: 32 GB of a 43 GB 4K HEVC remux for
645 cells. A Sheets list over a Matroska input therefore starts each entry
at the sample's keyframe and reads only it and the entry's span
(`keyframes.go`):

- `Runner.Run` reads the keyframes once, before any attempt and its timeout,
  from the file's Cues with `keyframes.ReadMatroska`: the index the
  demuxer's seek uses, plus its spot check of a few clusters' block headers,
  in-process and without decoding. The read runs in a goroutine, and the run
  stops waiting after 10 seconds or when its context ends, which also stops
  the read at its next call; a read blocked on stalled storage is left
  behind. At most 4 reads are in flight in a process, blocked ones
  included, which also bounds the memory parsing Cues takes (a few times
  their size), and a run that finds none free skips the read. A file that is not
  Matroska, has no Cues, whose Cues leave out keyframes, or whose read is
  skipped or stopped gets sample-time entries.
- An entry starts at the last keyframe at or before the sample's inpoint
  (sample time plus container start, in the microseconds the list is
  written in), rounded up to the microsecond so the seek never lands on the
  keyframe before it. On ffmpeg-muxed files with B-frames and with container
  starts of 3.21 and 11.4 s, this matched where ffprobe's
  `-read_intervals` seek, which makes the same seek call, landed for every
  sample. Samples before the first keyframe keep sample-time entries.
- A sample whose keyframe is the previous sample's gets no entry: it would
  only decode the frame the previous cell shows, and the HEVC decoder drops
  a keyframe sent twice in a row as a duplicate picture order count. Once
  the list is read, such a sample shows the previous cell's frame and its
  source time, so the repair below finds it repeating that cell and decodes
  its own moment. A distinct keyframe keeps its entry even when it lies
  inside the previous entry's span.
- Every frame of a keyframe entry lies at or after its inpoint, so its time
  places it back in the input exactly. A sample-time entry decodes the
  keyframe from before its inpoint, where ffmpeg rewrites the time to keep
  the list's timestamps monotonic; with samples sharing a 40-second GOP,
  that hid a repeat from the repair and left a cell showing the scene before.
- An open-GOP keyframe after a jump takes a picture order count derived
  from the last picture whose header the decoder parsed, skipped pictures
  included. Keyframe entries leave longer gaps between parsed pictures, and
  across a gap of more than half the count's cycle (128 frames with x265's
  default 8-bit count) the derived counts run backwards: the decoder passes
  on keyframes out of list order with rewritten times, or drops them as
  duplicates. As soon as a frame comes from an earlier entry than the frame
  before it, or the decoder logs a dropped keyframe ("Duplicate POC in a
  sequence", or H.264's "illegal short term buffer state"), the attempt
  stops ffmpeg and reads the samples from their own times, exactly as a
  file without Cues. An abandoned read is never published: if the
  attempt's time ran out meanwhile, it fails as `timeout`. A drop in the
  abandoned read does not count toward the window fallbacks above.
  Closed-GOP keyframes start their counts afresh and keep their keyframe
  entries.
- MP4 and every other container keep sample-time entries: an MP4 entry that
  starts exactly at its keyframe's time often seeks past it (45 of 80
  samples of one H.264 episode decoded nothing). Stats lists and repairs
  keep sample-time entries too.

Over Matroska test encodes (x264 and x265 at default settings and with
scene cuts off at 5, 10, 20, and 40-second GOPs, open- and closed-GOP HEVC,
samples every 5 and 10 seconds) keyframe entries never placed more wrong
cells than sample-time entries, and fewer where samples share long GOPs.
Open-GOP HEVC with an 8-bit count falls back once its listed keyframes lie
more than about 5 seconds apart, which covers x265's default 10-second GOP at
either interval; it then reads what it did before, plus the few entries read
before the fallback.

### Repairs

A list gives each sample the keyframe at or before its time, placed back in
the input from its frame time: `pts + start - k*span + inpoint - container
start` for list entry `k`, where `start` is the list input's start time from
its header, by which ffmpeg shifted every frame, and `inpoint` is where the
entry starts in the container. Two kinds of cell come out worse than the
input allows, and the attempt reads their samples again in a second list,
after the first run and before the sheets are encoded (`sheet_repair.go`):

- Missing cells decoded no frame. The demuxer could not seek to a keyframe
  before the sample, as in a container that flags no keyframes (VC-1 in
  Matroska, whose I-frames only the decoder recognizes, lost a third of its
  samples to half-second entries), or the HEVC decoder dropped the keyframe
  as a duplicate picture order count. The repair lists them again,
  keyframes only (with the codec's options above), each entry lasting until the next sample (the shortest gap
  between the run's samples): the decoder starts afresh and finds the
  keyframe before the sample, or failing that the first one after it.
- Repeated cells show the same moment as the cell before: their frames lie
  within a tenth of the time between the two samples, because no keyframe
  lies between them (a GOP longer than the interval, including every sample
  a keyframe list left out), or because an open-GOP keyframe brought a
  leading picture just before it that one sample kept.
  The repair lists them again with `-skip_frame:v noref`, decoding every
  reference frame from the keyframe on, and keeps the last frame at or before
  each sample's time.

Each repaired sample keeps its nearest frame: the last at or before its
time, else the first after it. A repair that ffmpeg finishes must line up
with its log like any run; one cut short keeps the frames it placed, which
were written and logged in order, and leaves the rest. Both repairs share one
time box, 30 seconds plus a quarter second a sample of the run, within the
attempt's timeout: on a 4K HEVC clip with a keyframe every 25 seconds and
samples every 10, software repaired 12 of 31 cells in 26 seconds, CUDA in 4.
Missing cells are repaired first, then the repeated cells, recomputed so a
recovered cell that shows its neighbor's keyframe is decoded at its own time
too. A list that decoded nothing is repaired as well, since a container
without keyframe flags can miss every short entry. A repair never fails the
run, except that the caller canceling ends it as `canceled`; the sparse check then counts what the run and its repairs
decoded, so a list whose repairs recover its missing samples no longer needs
the window fallback above.

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

Chapter thumbnail arguments, attempt timeouts, and reasons are pinned by a
golden test in `internal/chapterthumbs` (`testdata/extract_argv_golden.json`)
recorded from the extractor they replaced; changing them changes the pixels
of new thumbnails.

## Capabilities

`LoadCapabilities` lists an ffmpeg binary's filters and muxers, and checks
that the chromaprint muxer can write raw fingerprints. `Capabilities.Require`
reports the first thing a request needs that the binary lacks: the
chromaprint muxer for a fingerprint, `silencedetect` for silence, and
`blackframe`, `signalstats`, and `metadata` for `Stats`, and `metadata`
for `Sheets`. Images need no check up front; the runner reads the tone-map filters itself (see
[Images](#images)).

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
waits for a schema maintenance window. `internal/mediaartifact` owns the
table: `mediaartifact.Store` reads and writes it through `Load`, `LoadMany`,
`Upsert`, and `RecordFailure`, and `Artifact.State` interprets a stored row.
Features consume it by kind; intro detection (`internal/intromarkers`) is one
consumer and stores the `intro_fingerprint`, `credits_fingerprint`, and
`credits_tail` kinds.

- **Key.** The primary key is `(media_file_id, algorithm_version,
  config_hash)`. A row also has a `kind`, such as `intro_fingerprint`. Kinds
  never share a key because each derives its `config_hash` with
  `mediaartifact.ConfigHash`, a hash of the kind and its parameters. Intro
  fingerprints keep `intromarkers.Config.ConfigHash`, which predates the
  namespacing and is pinned by a test. An upsert never takes over another
  kind's row.
- **Identity.** Each row records the file hash, size, duration, and analysis
  window it was computed from. A row applies only while all of them match the
  file.
- **Payload.** `points` holds the payload bytes and `point_count` the number
  of items in it; `fingerprint_format` names the encoding. Each kind owns its
  payload encoding and lives with the feature that consumes it;
  `mediaartifact` stores the bytes without interpreting them.

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
