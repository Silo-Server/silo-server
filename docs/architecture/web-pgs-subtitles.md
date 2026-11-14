# Web PGS subtitles

The web player draws embedded PGS (Blu-ray bitmap) subtitles itself instead of
asking the server to burn them into a transcode. Paths are
repository-relative.

## Contract

The web player declares `embedded_bitmap: true` on every delivery class
(`web/src/player/client-context-v3.ts`). The server then keeps the plan's
delivery and publishes the embedded PGS track as a lossless `.sup` sidecar
(`subtitle.mode: "render"`), the same way it publishes embedded text tracks
(see [playback-protocol-v3.md](playback-protocol-v3.md) §8).
`embedded_bitmap` covers embedded PGS only. The server still burns in embedded
DVD (VobSub) and DVB bitmaps, and refuses external bitmap files
(`subtitle_burn_in_source_unsupported`). `sidecar_bitmap` stays `false`.

Auto-select and the initial start ask the same question through
`subtitleNeedsBurnIn` (`web/src/player/utils/subtitleCodecs.ts`): embedded
PGS does not need the server, so it is not ranked as a burn-in and a refused
start is not blamed on it.

## Rendering

- `web/src/player/utils/pgs.ts` parses the `.sup` stream: segments, palettes
  (YCbCr to RGBA, BT.709 for HD and BT.601 for SD), run-length objects,
  windows, object cropping and composition states. It parses incrementally
  and looks up the display set active at a media time.
- Palettes and objects persist through an epoch, so a display set can place
  objects an earlier one defined. A display set whose objects or palette the
  parser never saw is dropped, never treated as a clear.
- `web/src/player/hooks/usePGSSubtitles.ts` fetches positioned windows
  (`windowed=1&position=&duration=`) through the shared window fetcher
  (`web/src/player/utils/subtitleWindows.ts`), so seeks far into a file do not
  download the whole track. Every request is one whole, grid-aligned 120-second
  page: `position` is the playhead floored onto the source-zero page grid and
  `duration` is the full page (`BITMAP_SUBTITLE_PAGE_SECONDS`), the same page
  the server stores and prefetches. A request anchored on the raw playhead is a
  range no other request computes, so nothing is ever reused and each page
  boundary waits on a fresh container read. The fetcher asks for the next page
  30 seconds before the current one ends; an extend requests the page that
  contains the committed coverage, never a page already fully held, and keeps the
  parser's epoch state, while a fresh window starts a new parser. A response's
  `X-Subtitle-Coverage` bounds what it committed: a page the server could answer
  only in part commits that shorter range and asks for the same page again, so
  the tail it did not send is never skipped.
- A screen stays up until the next display set replaces it, but never past
  what has loaded: the end of the last finished window or, beyond it, the
  latest segment read from the window on the wire. Screens more than one page
  behind the playhead are dropped.
- A screen whose epoch began before the fetched window cannot be reconstructed
  from that window. While playback runs this is covered by the pages already
  loaded: a page starts at or before the playhead, and the previous pages stay in
  the timeline. A seek into the first seconds of a page can miss a composition
  defined in the previous page until the next display set; that follows from the
  stream itself, since the window must contain the palette and object
  definitions, not just the placement.
- Each composition is drawn on a canvas that spans the player, scaled from the
  PGS video size onto the picture rectangle (`web/src/player/utils/videoFit.ts`)
  for both contain and cover fits. The plane matches the picture along its
  uncropped side, so subtitles authored in a letterbox or pillarbox stay
  there. Objects stay inside the player, shrinking when they do not fit (a
  Fill crop, or a tall object over a short player), and lower-half objects
  stay above the control bar while it is up, whatever the text position
  setting says.
- The subtitle delay and the stream's timeline origin apply as they do for text
  cues.
- A plan whose subtitle mode is still `burn_in` is left to the server, and the
  overlay stays off so the subtitle is not drawn twice.

## Failure and recovery

- A `409 subtitle_source_changed` means the virtual release rotated and the
  plan's subtitle URLs are stale. The fetcher stops that URL without spending a
  retry, the player refreshes the subtitle inventory, and the refreshed
  generation rebuilds the parser and window. If the refresh re-mints the same
  URL, the refresh revision still forces one retry; a repeated conflict ends in
  the visible error state rather than an endless "loading" overlay.
- A `503 subtitle_source_unavailable` carries `Retry-After`. The client waits at
  least that long, then uses its normal capped backoff. Four consecutive
  non-retryable failures or twelve retryable failures end the fetch for that
  mount; a seek, track change, replay, or manual retry starts a new window.
  `503 subtitle_page_not_ready` is the same retryable shape for a page whose
  start the server's progressive bitmap artifact has not parsed yet: it is a
  wait, not a failure of the source.
- On the server, windowed `.sup` extraction uses the same last-good window
  cache, in-flight coalescing, and per-identity extraction cooldown as text
  windows. A repeated or concurrent window does not spawn a second ffmpeg, and
  an upstream failure can answer 503 without reopening the source. Full-track
  `.sup` caching and Range/HEAD behavior are unchanged.

## Bitmap pages on a large relay source

A windowed PGS extract cannot seek a non-seekable relay, so ffmpeg reads the
container from byte 0 and discards the prefix. A page at offset S therefore costs
`(S + W) × bitrate`, not `W × bitrate`: a 120 s page of an 18.25 GB / 35 Mbps
source is ~525 MB, which is also the window extraction timeout. What hurts is the
prefix, and it is not only bytes: on a relay that answers the seek with range
reads a page at 3600 s read under the byte cap and still took 88 s. Three rules
follow.

- One unbounded `.sup` demux runs per (source identity, track, format
  generation), single-flighted through the cache's warm slot and detached relay
  budget. It writes the track's artifact and publishes how far the display sets
  it has parsed reach (a coverage watermark), so a page inside that coverage is
  a local read of the artifact rather than another source read. The watermark
  advances only at a complete display set (`PCS … END`), and a page the artifact
  has parsed is never answered with a truncated run.

  A page whose start is parsed but whose end is still ahead of the watermark is
  served from the parsed prefix: the response is a 200 and `X-Subtitle-Coverage`
  reports the shorter range it really produced, so the client keeps only that
  range and asks for the page again — it never treats the unparsed tail as
  delivered. A page that starts at or past the watermark answers
  `503 subtitle_page_not_ready` with `Retry-After` and the covered range; that
  code is distinct from `subtitle_source_unavailable` because the source was never
  read. A `.sup` fill flips into the committed whole-track entry when its extract
  reaches end of file.
- A bounded page that misses the cache starts that fill while the viewer is
  playing. The fill is the page path — it produces the pages the client asks for
  next — so it replaces the bounded window prefetch for bitmap pages. The
  complete-track bitmap warm for any other request (a whole-track fetch, or an
  open-ended window) stays paused-only. The fill is single-flighted, so a burst
  of page requests starts exactly one.
- Before a page reads its source, the server prices the prefix that read would
  discard — `start × rate`, from the row's size and runtime or bitrate, and in
  seconds from the rate this identity's extracts have measured. A page beyond
  256 MB of prefix, or one whose prefix would take more than 45 s at the measured
  rate, is refused before the extract with `503 subtitle_page_too_costly`,
  `Retry-After` and `X-Subtitle-Coverage`, and that refusal starts the fill. An
  opening page has no prefix and always extracts. A page extract that does run is
  still capped at 256 MB of process reads; one killed that way, or one that runs
  out the two-minute window extract deadline before emitting a byte, answers the
  same retryable shape instead of a 500.

All of it applies only to a remote identity, and pricing only to one whose size
and runtime (or bitrate) are known: an unpriceable page keeps its source extract
rather than being refused for an assumption. A text (WebVTT/SRT/ASS) window and a
local file keep their existing paths; a windowed text extract is held until it
completes, so a slice cut short by the deadline or a canceled request is never
served as a 200 with half a cue.

## Jellyfin compatibility

The Jellyfin surface burns embedded PGS into the transcode and does not publish
a `.sup` sidecar. That is deliberate parity with Jellyfin clients: they do not
render web PGS. The web player uses the sidecar path described above.

## Tests

`web/src/player/testdata/pgs/dam-cav.sup` is a real 1280x720 PGS stream with a
display set at 0 seconds and a clear at 60 seconds. Parser tests and the
pipeline test use it, so the renderer is exercised against real segments rather
than only hand-built ones.
