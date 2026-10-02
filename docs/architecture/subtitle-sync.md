# Subtitle sync

Subtitle sync aligns a stored subtitle (`downloaded_subtitles`) to its media
file's audio. Most "out of sync" provider subtitles were cut for another
release of the title: a 25 fps PAL speed-up of a 23.976 fps film, an extra
studio logo or recap, or a different container start. Sync finds the timing
correction that fixes those cases, and reports `no_match` when the subtitle
does not line up with the audio at all, which usually means it belongs to a
different release or title.

The code lives in `internal/subtitles/subsync`; the API is described in
[subtitles-api.md](../subtitles-api.md#stored-subtitle-sync).

## Timing correction

A correction is `Timing{Scale, OffsetMS}` (`internal/subtitles/timing.go`):
original time `t` plays at `t * scale + offset_ms`. It is stored on the
subtitle row (`timing_scale`, `timing_offset_ms`); the bytes never change.

- Every client delivery path applies the row's timing to the stored bytes
  before any conversion: the playback sidecar, the Jellyfin subtitle stream,
  offline downloads, and the source of an AI translation
  (`subtitles.DeliveryBytes`). The administrator download returns the stored
  bytes unchanged, and content identity and deduplication use them too.
- `Retime` rewrites SRT, WebVTT, and ASS/SSA timestamps and leaves every other
  byte as stored. Under a scale it also scales the event-relative times in ASS
  override tags (karaoke `\k` syllables, `\t`, `\move`, `\fad`, `\fade`),
  so effects keep pace with their stretched event. Other formats cannot be synced or retimed.
- A timing change bumps the row's revision like any other update, so
  validators captured before it go stale.
- Players already apply a per-device delay (`player.subtitle_sync_ms`); it
  stacks on top of the stored correction.

## Alignment

1. **Speech levels.** For each sampled window, ffmpeg decodes one audio
   stream (the one in the subtitle's language, else the default), band-limits
   it to the speech band, and the runner reduces it to one level per 10 ms
   (`mediasample` speech output). A 5.1 or 7.1 stream is read from its centre
   channel, which carries the dialogue; a file whose centre channel cannot be
   read falls back to a downmix of every channel.
2. **Windows.** Files of 45 minutes or more are sampled as 12 windows of
   2 minutes spread over the runtime. ffmpeg's input seek reads only those
   stretches, so a large remux is not read end to end. Shorter files are read
   whole, window by window.
3. **Per-window lag.** For each framerate ratio tried (1, and 25, 24, and
   23.976 fps conversions either way), the subtitle's cues become an on/off
   map. Each window's speech is cross-correlated with it over ±10 minutes,
   coarsely with an FFT and then frame by frame around the best lag. A window
   counts only when it has enough subtitle text in range.
4. **Agreement.** A single window's best lag means little: on real audio a
   right lag often stands only a few standard deviations above the rest, no
   more than a wrong one. Wrong lags scatter over the whole search range,
   while right ones line up. The fit finds the line, offset plus a small drift
   of at most 0.2%, through the most window lags within 250 ms. A constant
   offset is preferred whenever it explains the same windows. Drift covers
   releases cut from different masters that differ by a few hundredths of a
   percent without any standard framerate conversion.
5. **Refinement.** The agreeing windows' correlations are summed over ±0.5 s
   around the fitted line, and the peak sets the final offset. A fitted scale
   the windows cannot tell apart from a standard ratio is snapped to it.
6. **Decision.** The result is `no_match` unless at least 3 windows and a
   quarter of those with a lag agree, and their lags stand out from the rest
   of their search ranges by a mean of 3.75 standard deviations. A result
   within 150 ms of the current correction over the runtime is
   `already_synced`; anything else is `synced` and is applied.

On real files, measured against subtitles whose timing is known (the file's
own embedded track, or provider subtitles matched to it by text), corrections
landed within about 120 ms. Subtitles of another title got at most two
agreeing windows. Cues are spotted slightly ahead of speech and stay up after
it, which bounds the precision and sets the `already_synced` margin.

Alignment runs against the original bytes, so syncing again never compounds a
correction. Tuning constants, and the measurements behind them, live in
`subsync/align.go`.

## Cost

Decoding one file's speech takes about 45 CPU-seconds of ffmpeg, once per
file; the cached levels take about 150 KB. Each alignment then takes about
0.2 CPU-seconds on the API server. A server runs two sync jobs at a time.

## Jobs

`subtitle_sync_jobs` holds one row per attempt. At most one job per subtitle
is active (`pending` or `running`). Jobs run on the shared AI job runner
(`internal/ai/jobrunner`) with their own concurrency bound, heartbeats, and
stale-job reaping. A job reaped after a crash ends `failed`; it is not resumed.

- **Triggers.** A provider download or a user upload starts an automatic job
  when `subtitles.auto_sync` is on (the default). It runs only for a subtitle
  that has never been synced and still has its original timing: adding
  identical content again returns the existing row, and must not replace a
  timing someone set or reset. It skips formats that cannot be retimed. A
  manual job is started through the API.
- **Bounds.** Cues past the audio's reach or longer than a minute are left out
  of alignment, so a corrupt timestamp cannot size the cue map. A node request
  is bounded by the node's slot wait, its decode timeout, and a minute. A node
  that cannot read the file hands the work back to this server under
  `prefer_transcode_nodes`.
- **Guarded apply.** A job records the subtitle revision it aligned. It
  applies its result and finishes in one transaction only while the row still
  has that revision. If the subtitle changed meanwhile (a manual timing edit,
  a language change), the newer edit wins and the job ends `failed`.
- **Notification.** An applied result or a manual timing change sends
  `subtitle_timing_changed` to the file's playback sessions, on every API
  server through the event bus (`silo:playback`). It is best effort; clients
  also see the job state in the stored subtitle list.

## Where the audio is decoded

`subtitles.sync_execution` chooses where speech levels are decoded:

| Value | Behavior |
|---|---|
| `prefer_transcode_nodes` (default) | Decode on an enabled, healthy transcode node; fall back to this server when none has capacity or the node fails for reasons of its own (unreachable, missing ffmpeg capability). |
| `transcode_nodes_only` | Decode only on a transcode node; fail the job when none is available. |
| `local` | Decode on this server. |

A file's windows run on one node. Each API server picks the least loaded
node through `nodepool.Reservations`, and the node itself admits at most
`subtitles.sync_node_capacity` sampling runs at once, whichever API servers
send them. A request over the limit waits up to two minutes for a slot,
then is refused as unavailable, which `prefer_transcode_nodes` answers by
decoding on this server.
The node runs each window through `POST /media-samples/run` (see
[media sampling](media-sampling.md#remote-runs)). The input path must be one
the node is allowed to read, which requires the same media paths on the node
as on the API server.

## Speech level cache

Speech levels are cached as `subtitle_speech` media analysis artifacts
(`internal/mediaartifact`), keyed by audio stream, channel choice, and window
plan, and tied to the file's hash, size, and duration. A second subtitle for
the same file aligns without decoding. A file with no usable stream records an
`unusable` artifact; a transient failure records `failed` with the artifact
store's backoff, which automatic jobs respect and manual jobs bypass.
