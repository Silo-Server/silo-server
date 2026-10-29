# Reading embedded subtitles through the container index

A subtitle track's packets are spread through the whole media file, so
extracting one with FFmpeg reads all of it: minutes on network storage.
`internal/embeddedsub` reads only the track's packets, found through the
file's own index, and hands them to the same FFmpeg conversion so the output
bytes don't change.

## What it reads

- **Matroska.** `ReadIndex` reads the EBML header, the seek heads (following
  one that points at another), Info, Tracks, Tags and Cues. It never walks a
  cluster, so Cues stored after the clusters without a seek head entry are
  not found. `ReadPackets` reads, per cue, the cluster's head (its Timestamp,
  once per cluster) and the block at the cue's CueRelativePosition. The two
  reads go out together: the block read starts where the block would be with
  the shortest cluster header and covers the seven bytes a longer one adds.
  A cue without CueRelativePosition costs a walk of its cluster's element
  headers instead.
- **MP4 and MOV.** `ReadIndex` walks the top-level boxes to `moov` and
  expands each text track's sample table (stsd, stts, stsc, stsz, stco or
  co64). Edit lists are mapped when they are leading empty edits plus one
  edit at normal rate. Fragmented files, composition offsets and sample
  entries other than tx3g and QuickTime text are reported as unsupported.

The parsing primitives shared with `mediaprobe` and `playback/keyframes` live
in `internal/matroska`.

## Completeness

A Matroska track's cues are trusted only as far as the file can vouch for
them. `Completeness` is `complete` when the cue count equals the muxer's
NUMBER_OF_FRAMES statistics tag, `incomplete` when it differs, `unknown`
without the tag (FFmpeg never writes it; FFmpeg-muxed files cue every
subtitle packet), and `none` without cues. `ReadPackets` refuses `none`
(`ErrNoCues`) and, unless asked, `incomplete` (`ErrIncompleteCues`); callers
fall back to demuxing the whole file. A cue that points at data that isn't
the track's block, is cut off, or doesn't decompress is reported in a
`DamagedError` alongside the packets that were read. Laced blocks count as
one cue but several frames, so a laced track tagged with NUMBER_OF_FRAMES
rates `incomplete`.

## Same output as today's extraction

Packets go back to FFmpeg as a one-track file on stdin, written by
`WriteStream`: a subtitle-only Matroska stream (`WriteMKS`) or a one-track MP4
that copies the source's ftyp, mvhd, tkhd, edts, mdhd, hdlr and stsd boxes.
`playback.StreamExtractSubtitle` (with `StreamExtractOpts.Packets`) and
`ExtractSubtitlePacketsWithFormat` run the same output options on it as on
the source. The tests compare the bytes against FFmpeg's extraction from the
source for SubRip to WebVTT and SubRip, ASS to ASS and WebVTT, PGS to SUP,
and mov_text to WebVTT and SubRip. Invariants that keep them equal:

- **Timestamps.** A demux of the source subtracts the source's start time
  (ffprobe's `format.start_time`, from all streams), and a subtitle-only
  stream has none of its own. The conversion runs with
  `-copyts -itsoffset -<start time>`, so the same offset applies.
  `playback.SubtitleSourceStartTime` reads it with ffprobe, which reads only
  what FFmpeg probes at the head of the file; keep it with the index.
- **Block layout.** `WriteMKS` writes each packet with the duration and
  keyframe flag FFmpeg read from the source block, and leaves out a duration
  that came from the cue or the track default. The frames of one laced block
  go back into one laced block: FFmpeg keeps a laced block in one buffer, and
  its SubRip decoder reads a frame's text on into the frames after it.
- **Order.** Packets are written in file order, the order FFmpeg demuxes.
- **MP4 times.** Samples keep their decode times in media ticks; a gap before
  or between a window's samples is filled with empty text samples.

When a cue starts before the source's start time, FFmpeg's muxer shifts the
whole output later so no timestamp is negative (`avoid_negative_ts`). Today's
extraction does it too, so the whole-track output matches, but a window that
leaves that cue out isn't shifted. Windows can also differ from FFmpeg's own
seek through the source, which lands on whatever packet precedes the seek
point; an empty window converts to the WebVTT header alone.

## Windows

`ReadPackets(from, to)` returns the packets that show at any time in
`[from, to)` on the container's timeline, before the start time is
subtracted. A packet without a duration shows until the next one starts,
so the last packet to start by `from` is always included. Cues that carry
CueDuration bound the lookback exactly; otherwise it starts at 30 s and widens
when a block read turns out to last longer, up to 10 minutes, so a long
packet that started earlier than any packet read can still be missed. The
frames of a laced block are kept or dropped together, since FFmpeg converts
each frame with the ones after it in view.

## Use

An `Index` is immutable and safe for concurrent use; `Cache` keeps them by
path, size and modification time. `ReadPackets` opens the file itself, sets
`POSIX_FADV_RANDOM` on Linux so the kernel doesn't read ahead around each
small read, keeps up to 16 reads in flight, holds at most 256 MiB of packet
data by default (`ErrTooLarge`), and refuses a file whose size or
modification time changed since it was indexed (`ErrFileChanged`).

HLS streams read their on-demand subtitle renditions through it
(`playback/subtitle_rendition_tracks.go`): one window per segment, from a
process-wide `Cache`, converted by `playback.ExtractSubtitlePacketsWebVTT`
with the options of the stream's own cue output. The planner checks with
`playback.CueIndexedSubtitleTracks` which tracks qualify before it lists them.

Errors tell the caller what to do. `ErrNotSupportedContainer`, `ErrMalformed`,
`ErrNoCues`, `ErrIncompleteCues`, `ErrUnsupported` and `ErrTooLarge` describe
the file: fall back to FFmpeg. `ErrFileChanged` means index the file again. A
`DamagedError` comes with the packets that were readable; a whole-track
reader falls back. Any other error is a failed read or the caller's context.

## Kept tracks

A track read whole is kept, so reading it costs the source once on any node.
`internal/keptsub` stores it in the assets blob store
([blob-storage.md](blob-storage.md)) as
`subtitles/embedded/<media_files.id>/<stream index>-<sha256>.<mks|mp4>` and
records it in `embedded_subtitle_tracks`, one row per file and probed stream
index.

The stored bytes are a subtitle-only stream FFmpeg converts in milliseconds,
with the same output options as a demux of the source, so every format,
window and offset comes from the existing conversion:

- **Read through the index** (`method = cues`): `WriteKept`, which is
  `WriteStream` plus, for Matroska, a Cues element ahead of the clusters with
  an entry and a CueDuration for every block, so `ReadIndex` reads the kept
  stream back to the same packets and windows can be cut from it. An MP4
  stream has its sample table anyway. Timestamps stay on the source's
  timeline and `start_time_us` holds the source's start time, which the
  conversion subtracts.
- **Demuxed** (`method = ffmpeg`), for a file the index can't read: the
  demux that serves the request also copies the track, unconverted
  (`-c:s copy`, Matroska, or MP4 for `mov_text`), into a temporary file.
  `-output_ts_offset` adds the start time the demux took off back, so the
  copy is on the source's timeline too and a muxer has no negative timestamp
  to shift. An MP4 copy is written with `+faststart`: it is converted from a
  pipe, where FFmpeg can't seek back to a moov written after the samples and
  converts nothing past the first few dozen kilobytes. FFmpeg writes Cues for every subtitle packet of a file it can
  seek in. A demux whose copy fails runs again without it, so the copy never
  costs a viewer the track.

The row records the source version read (`source_size`, `source_mtime_ns`),
the codec and `container_track_id`, the key, SHA-256, size and packet count,
`completeness` (`complete` when the cues matched NUMBER_OF_FRAMES or FFmpeg
demuxed the file, else `unknown`), and `has_override_tags` for plain-text
tracks (`embeddedsub.HasOverrideTags`; NULL for ASS and PGS).

**Invalidation.** A reader compares the row with a stat of the source and
treats any other size or modification time, container track ID or codec as
no row; the next whole read replaces it. A catalog update that changes
`media_files.file_size` or `file_modified_at` deletes the file's rows.
Override-tag facts also require the row's size to match the catalog's.

**Garbage.** Database triggers queue a stream's key in `blob_gc_queue` a day
out when its row is replaced, invalidated or deleted, including by the cascade
from a deleted media file. `keptsub.StreamBlobNamespace` lets the collector
delete a key no row names; `keptsub.FileBlobNamespace` lets the orphan sweep
queue a deleted file's prefix. A row whose object is missing, as after a
storage transition that left objects behind, is deleted and the track read
again.

A conversion of a kept track, which holds packets, that yields no cue is a
failure however FFmpeg exited: the request demuxes the source instead, and
the empty result is neither served nor cached.

**Local copies.** A node copies a kept stream it reads to
`<transcode dir>/subtitle-cache/kept/<sha256>.<ext>` and serves from there; a
fetched copy must match its hash. Copies never go stale, since the name is the
content. A copy's file is never touched after it is written, so the index
cache, keyed by path, size and modification time, keeps its index; recency is
kept in memory, and copies are held to 1 GiB, least recently used first. A
node without a transcode directory keeps no tracks.

**Storing.** A read writes the kept stream to a local copy, not to memory,
and the object is streamed from it. The key goes into `blob_gc_queue`, due a
day later, before the object is stored: if the row then fails to land, the
collector deletes the object, and if it lands, the collector finds the key
live and only dequeues it.

**One read at a time.** Concurrent calls for a track on a node share one
read. A prefetch starts its read at background priority and stops it when
its callers go; a request that joins it raises its I/O priority and
concurrency (`embeddedsub.Priority`). A request's read finishes and keeps the
track even after every caller has gone, so a client that gave up and
retries finds the track kept or still being read. Up to four whole reads run
on a node at once, each holding at most 128 MiB of packets; a larger track is
demuxed. Across nodes, a reader holds a claim in
`embedded_subtitle_track_claims` with a lease longer than its read and keep
can last, and releases it after. A request that finds another node's claim
polls for that node's row, or the claim coming free, for up to three
minutes, then reads anyway; a prefetch skips the track. The claim only saves work: there
is no fence, since a read that outlives its lease keeps an equally valid
track, and two writers of a track write the same key or replace each other's
row.

**Timing.** Waiting on another node and the read itself are each bounded to
three minutes, and storing to one. A request waits on the read for at most
90 seconds, well inside the response's write stall window, which runs until
the first byte; it then demuxes the source, which streams its first cues at
once, while the read goes on and keeps the track.

**The first byte.** Players give up on a silent response sooner than the
server does: Android's player client reads with a 30-second timeout, and
Apple's player gives up after about a minute. Every WebVTT output starts
with `WEBVTT\n`, so while a WebVTT response's output is late, those seven
bytes go ahead one at a time, the first after five seconds and the rest
twelve seconds apart, and are dropped from the output when it comes, which
must start with them. The response stays byte-identical. A failure after the
first of them aborts the response instead of answering 500. ASS and SUP
output have no constant prefix (an ASS header is the track's own, and a SUP
stream starts with a timestamp), so their first byte waits on the read, as
does a SubRip or ASS answer that jellycompat or translation builds whole
before writing it; a client that times out leaves the read running, and its
retry finds the track kept or joins the read.

## Serving

`playback.SubtitleCache.ServeExtract` serves an embedded track that a request
names (`StreamExtractOpts.Embedded`), in order:

1. a whole track converted earlier, from the node's subtitle cache;
2. a fill that converts the kept track, and commits the result to the cache;
3. a fill that reads the track whole through the index, keeps it, converts
   it, and commits;
4. a fill that demuxes the source as before, copying the track to keep.

A conversion is held until it completes (or passes 16 MiB), so one that fails
before anything reaches the response falls back to the demux. Windows
(`position`/`duration`, and PGS `windowed=1`) come from the kept track, else
from `ReadPackets` for just the window, else from FFmpeg's seek through the
source as before. A window open to the end (a position without a duration)
covers most of the track, so it is read whole and kept first, as a
whole-track request would; a PGS one is answered from the node's cached
`.sup` first, when there is one. A PGS window answered from the source warms
the cached `.sup` in the background, as a demuxed one did, which keeps the
track too. Only a file without usable cues and without a kept track
still runs FFmpeg over the source for a window.

The native route, offline downloads' embedded sidecars and AI translation
share the API's cache; jellycompat's cache uses the same store, for track
requests and for the warm at PlaybackInfo, which demuxes only the tracks the
index can't read. The standalone proxy has neither database nor blob storage
and demuxes as before.

## Facts and prefetch

`keptsub.Store.SubtitleTrackFacts` answers planning's
`SubtitleTrackFactsV3` from the kept rows: the first `OverrideTags` question
of a plan reads the file's rows in one query, and a plan whose client declares
no limitation makes none. A track without a row, or with a row for another
version of the file, is unknown.

`keptsub.Prefetcher` runs at a v3 start: for a client that declares
`literal_override_tags`, the playing file's plain-text tracks; and the next
available episode's text tracks in the language of the subtitle the plan
selected (`EpisodeRepository.ListAdjacentInSeries`). It reads through the
index only, never demuxes, at idle I/O priority with four reads in flight,
two jobs server-wide and at most eight tracks a job, skips a track another
node is reading, remembers for an hour a track it couldn't read, and cancels
a session's job when the session ends.

