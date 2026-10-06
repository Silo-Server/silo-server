# Subtitle sync

Subtitle sync aligns a subtitle to its media file's audio: a stored subtitle
(`downloaded_subtitles`) or a subtitle file next to the media (a sidecar). Most "out of sync" provider subtitles were cut for another
release of the title: a 25 fps PAL speed-up of a 23.976 fps film, an extra
studio logo or recap, or a different container start. Sync finds the timing
correction that fixes those cases, and reports `no_match` when the subtitle
does not line up with the audio at all, which usually means it belongs to a
different release or title.

The code lives in `internal/subtitles/subsync`; the API is described in
[subtitles-api.md](../subtitles-api.md#stored-subtitle-sync).

## Timing correction

A correction is `Timing{Scale, OffsetMS}` (`internal/subtitles/timing.go`):
original time `t` plays at `t * scale + offset_ms`. A stored subtitle keeps it
on its row (`timing_scale`, `timing_offset_ms`); a sidecar's lives in
`external_subtitle_timings` (see [sidecar subtitles](#sidecar-subtitles)). The
bytes never change.

- Every client delivery path applies the timing to the original bytes before
  any conversion: the playback subtitle route, the Jellyfin subtitle stream,
  offline downloads, and the source of an AI translation
  (`subtitles.DeliveryBytes` for stored subtitles, `playback.LoadExternalSubtitle`
  for sidecars). Those responses are `Cache-Control: private, no-cache`, since
  the bytes change behind the same URL. The administrator download returns the
  stored bytes unchanged, and content identity and deduplication use them too.
- `Retime` rewrites SRT, WebVTT, and ASS/SSA timestamps and leaves every other
  byte as stored. Under a scale it also scales the event-relative times in ASS
  override tags (karaoke `\k` syllables, `\t`, `\move`, `\fad`, `\fade`),
  so effects keep pace with their stretched event. Other formats cannot be synced or retimed.
- A timing change bumps the row's revision like any other update, so
  validators captured before it go stale.
- Players already apply a per-device delay (`player.subtitle_sync_ms`); it
  stacks on top of the stored correction.

## Sidecar subtitles

Silo is a media server, not a media manager: it never writes a sidecar file.
A sidecar's correction is a row keyed by the media file and the SHA-256 of the
sidecar's bytes.

- **Identity.** Clients name a sidecar by its sync key,
  `external-{sha256 of its path}`, the same path key playback URLs pin it with
  (`external_subtitle_key`). The server resolves the key against the file's
  scanned sidecars and reads the bytes to find the row.
- **Edits and renames.** A sidecar edited or replaced on disk has other bytes,
  so its old correction no longer applies; a renamed one still matches. The
  row records the last path a sync saw (`path`); recording a new path does not
  change the row's revision.
- **Delivery.** `playback.LoadExternalSubtitle` reads the file, hashes it, and
  applies the matching correction. Only SRT, WebVTT, ASS, and SSA sidecars can
  be corrected; other formats are served as before.
- **On first play.** Nothing syncs sidecars in bulk: not a scan, not a
  scheduled task; a tool that rewrites subtitle files (Bazarr) covers that.
  A sidecar is synced when someone asks, or automatically the first time a
  player is served it (see Triggers). A sync request creates the row with the
  original timing, so the job has a revision to guard its result with.
- **Jobs.** A job reads the sidecar from the row's path when it runs. It ends
  `failed` with `subtitle_changed` when the bytes no longer match the row, the
  file is gone, or the scanner no longer lists it under the media file.

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

`subtitle_sync_jobs` holds one row per attempt, for a stored subtitle
(`subtitle_id`) or a sidecar correction row (`external_timing_id`). At most
one job per subtitle is active (`pending` or `running`). Jobs run on the shared AI job runner
(`internal/ai/jobrunner`) with their own concurrency bound, heartbeats, and
stale-job reaping. A job reaped after a crash ends `failed`; it is not resumed.

- **Triggers.** With `subtitles.auto_sync` on (the default), an automatic job
  starts when a provider download or a user upload adds a subtitle, and when
  a player is first served a subtitle (stored or sidecar) through the playback
  or Jellyfin subtitle routes. Delivery hands the subtitle to the service and
  never waits for it (`subtitles.PlaySyncer`; HEAD requests do not count).
  Each server considers a played subtitle once every 30 minutes, since players
  fetch subtitles in windows. An automatic job runs only for a subtitle (or a
  sidecar's bytes) that has never been synced and still has its original
  timing: adding identical content again returns the existing row, and must
  not replace a timing someone set or reset. It skips formats that cannot be
  retimed. A manual job is started through the API.
- **Bounds.** Cues past the audio's reach or longer than a minute are left out
  of alignment, so a corrupt timestamp cannot size the cue map. A node request
  is bounded by the node's slot wait, its decode timeout, and a minute. A node
  that cannot read the file hands the work back to this server under
  `prefer_transcode_nodes`.
- **Guarded apply.** A job records the subtitle revision it aligned. It
  applies its result and finishes in one transaction only while the row still
  has that revision. If the subtitle changed meanwhile (a manual timing edit,
  a language change), the newer edit wins and the job ends `failed`.
- **Who may sync.** Anyone with access to the file may start a sync or set
  the timing. The bytes never change and the timing can always be reset, so
  no owner check applies; demo mode refuses both.
- **Replaced files.** When a media file's hash or size changes (both values
  known), a trigger on `media_files` resets its subtitles' timing and deletes
  their sync jobs. Deleting a running job stops it from applying: `Apply`
  finishes the job in the same transaction and rolls back when the row is gone.
- **Progress.** A running job records its `phase` (`analyzing` while it reads
  the file's speech, `matching` while it aligns cues) and `progress` (0..1) on
  the job row, which never touches the subtitle's revision. Decoding speech
  takes almost all of a job's time, so each decoded window advances progress;
  speech already cached skips straight to matching.
- **Failures.** A failed job names why in `failure`: `subtitle_changed`,
  `no_audio` (the file has no audio the decoder can read), `unavailable`
  (no node or server could decode it now, it backed off, or it was
  interrupted; a later attempt can pass), or `error`.
- **Notification.** Each step of a job (queued, every progress update, the
  outcome) sends `subtitle_sync_updated` to the file's playback sessions; an
  applied result or a manual timing change also sends
  `subtitle_timing_changed`. Both reach sessions on every API server through
  the event bus (`silo:playback`). They are best effort; clients also read the
  job state through the API.

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

## Client feedback

The web player shows a sync the viewer started (a sync they asked for, or the
automatic sync of a subtitle they downloaded or uploaded) in a card in its
top-right corner, which stays visible in fullscreen: the phase and progress
while it runs, then the outcome. A synced result for the track on screen reads
"Applying new timing" until the track's cues reload with the new timing, then
"Subtitles synced" with the correction. A timing change someone else made to
the track on screen shows a short note once the new cues load; the automatic
sync of a subtitle the first time it is played shows nothing. A timing reload
of the track on screen swaps its cues in place: the current cues stay up until
the corrected ones load (text tracks), or the corrected script is loaded into
the running renderer (ASS), with no loading notice in between. The subtitle
menu shows each track's status and the selected track's progress, last result,
and actions. Clients use the same states, from the API and the realtime events
(see [subtitles-api.md](../subtitles-api.md#subtitle-sync)).

## Speech level cache

Speech levels are cached as `subtitle_speech` media analysis artifacts
(`internal/mediaartifact`), keyed by audio stream, channel choice, and window
plan, and tied to the file's hash, size, and duration. A second subtitle for
the same file aligns without decoding. A file with no usable stream records an
`unusable` artifact; a transient failure records `failed` with the artifact
store's backoff, which automatic jobs respect and manual jobs bypass.
