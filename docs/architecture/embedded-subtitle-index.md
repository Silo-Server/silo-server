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
leaves that cue out isn't shifted.

## Windows

`ReadPackets(from, to)` returns the packets that show at any time in
`[from, to)` on the container's timeline, before the start time is
subtracted. A packet without a duration shows until the next one starts,
so the last packet to start by `from` is always included. Cues that carry
CueDuration bound the lookback exactly; otherwise it starts at 30 s and widens
when a block read turns out to last longer, up to 10 minutes.

## Use

An `Index` is immutable and safe for concurrent use; `Cache` keeps them by
path, size and modification time. `ReadPackets` opens the file itself, sets
`POSIX_FADV_RANDOM` on Linux so the kernel doesn't read ahead around each
small read, keeps up to 16 reads in flight, and refuses a file whose size or
modification time changed since it was indexed (`ErrFileChanged`).
