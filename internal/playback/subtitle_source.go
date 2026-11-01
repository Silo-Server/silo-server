package playback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

// EmbeddedSubtitleRef names an embedded subtitle track by what the catalog
// records about it.
type EmbeddedSubtitleRef struct {
	MediaFileID int
	// StreamIndex is the track's probed stream index
	// (models.SubtitleTrack.Index).
	StreamIndex int
	// ContainerTrackID is the Matroska TrackNumber or MP4 track ID, empty
	// when the scanner recorded none.
	ContainerTrackID string
	Codec            string
}

// EmbeddedSubtitleRefFor returns the ref of file's embedded subtitle track
// at ordinal, or nil when there is none.
func EmbeddedSubtitleRefFor(file *models.MediaFile, ordinal int) *EmbeddedSubtitleRef {
	if file == nil || ordinal < 0 || ordinal >= len(file.SubtitleTracks) {
		return nil
	}
	track := file.SubtitleTracks[ordinal]
	return &EmbeddedSubtitleRef{
		MediaFileID:      file.ID,
		StreamIndex:      track.Index,
		ContainerTrackID: track.ContainerTrackID,
		Codec:            track.Codec,
	}
}

// EmbeddedSubtitleSource reads embedded subtitle tracks without demuxing the
// whole source file: from a copy kept after an earlier read, or through the
// file's own index (embeddedsub). SubtitleCache asks it before running
// FFmpeg on the source. Any error means the caller demuxes the source as it
// did before.
type EmbeddedSubtitleSource interface {
	// WholeTrack returns the whole track as conversion input: the kept
	// copy, or the track read through the source's index, which it keeps.
	WholeTrack(ctx context.Context, path string, ref EmbeddedSubtitleRef) (*SubtitlePacketInput, error)
	// Window returns the packets that show at any time in [from, to) on
	// the playback timeline, which starts at the source's start time:
	// from the kept copy, or read through the source's index.
	// embeddedsub.WholeFrom and WholeTo leave a side open.
	Window(ctx context.Context, path string, ref EmbeddedSubtitleRef, from, to time.Duration) (*SubtitlePacketInput, error)
	// DemuxCopy says how a demux of the whole source copies the track for
	// KeepDemuxed: the FFmpeg muxer, and the source's start time, which the
	// copy's timestamps are shifted back by so they stay on the source's
	// timeline (a muxer would otherwise move a track with a cue before the
	// start time later). An error means the track isn't kept that way.
	DemuxCopy(ctx context.Context, path string, ref EmbeddedSubtitleRef) (DemuxCopy, error)
	// KeepDemuxed keeps the copy of the track that a demux of path wrote as
	// demuxCopy describes. srcSize and srcModTime are the source's when the
	// demux began. It may move the copy's file; the caller removes the file
	// if it is still there.
	KeepDemuxed(ctx context.Context, path string, ref EmbeddedSubtitleRef, demuxCopy DemuxCopy, srcSize int64, srcModTime time.Time) error
}

// ErrSubtitleReadsBusy means the node already admits as many whole-track
// subtitle reads as it allows. A request can retry after
// SubtitleReadsRetryAfter seconds.
var ErrSubtitleReadsBusy = errors.New("too many subtitle track reads")

// SubtitleReadsRetryAfter is the Retry-After, in seconds, of a request
// answered busy with ErrSubtitleReadsBusy.
const SubtitleReadsRetryAfter = 5

// DemuxCopy is a copy of a track that a demux of the whole source writes.
type DemuxCopy struct {
	// Path is the file the copy is written to.
	Path string
	// Format is the FFmpeg muxer: "matroska" or "mp4".
	Format string
	// StartTime is the source's start time, added back to the copy's
	// timestamps.
	StartTime time.Duration
}

// subtitleIndexCacheBytes bounds the embedded subtitle indexes the process
// keeps. An index is a few kilobytes to a few hundred.
const subtitleIndexCacheBytes = 64 << 20

// subtitleIndexes is the process's one cache of embedded subtitle indexes,
// of sources and of kept tracks alike: sidecar serving through kept tracks
// (keptsub), on-demand HLS renditions and the planner's check for them all
// read through it, so a file indexed for one is indexed for all.
var subtitleIndexes = embeddedsub.NewCache(subtitleIndexCacheBytes)

// SubtitleIndexes returns the process's cache of embedded subtitle indexes.
func SubtitleIndexes() *embeddedsub.Cache { return subtitleIndexes }

// SetEmbeddedSource makes the cache read embedded tracks through src before
// it demuxes their source. A nil cache ignores it.
func (c *SubtitleCache) SetEmbeddedSource(src EmbeddedSubtitleSource) {
	if c != nil {
		c.source = src
	}
}

const (
	// subtitleSpillBytes is how much converted output from a kept or
	// index-read track is held before any reaches the response, so a
	// conversion that fails can still fall back to demuxing the source.
	// It covers whole text tracks and windows; a whole PGS track can pass
	// it and then streams.
	subtitleSpillBytes = 16 << 20
	// keepDemuxedTimeout bounds keeping what a demux copied.
	keepDemuxedTimeout = 2 * time.Minute
)

// sourced wraps a whole-track extract so it reads the track from the
// cache's EmbeddedSubtitleSource first, and demuxes the source, keeping a
// copy, only when that can't. An extract whose options name no track, or
// that already has packets, runs as it is.
func (c *SubtitleCache) sourced(extract SUPExtractFunc) SUPExtractFunc {
	if c == nil || c.source == nil {
		return extract
	}
	return func(ctx context.Context, opts StreamExtractOpts) error {
		if opts.Embedded == nil || opts.Packets != nil {
			return extract(ctx, opts)
		}
		if _, format := streamExtractOutput(opts.SourceCodec, opts.TargetFormat); format == subtitleMuxerWebVTT {
			keepalive := newPrefixKeepalive(opts.Writer, webVTTPrefix, keepaliveDelay, keepaliveInterval)
			defer keepalive.stop()
			opts.Writer = keepalive
		}
		in, err := c.source.WholeTrack(ctx, opts.InputPath, *opts.Embedded)
		if errors.Is(err, ErrSubtitleReadsBusy) {
			// The node admits no more reads; a demux would add the load
			// the limit keeps off it.
			return err
		}
		if err == nil {
			wrote, convErr := convertPackets(ctx, opts.Writer, opts, in, extract)
			if convErr == nil || wrote || ctx.Err() != nil {
				return convErr
			}
			err = convErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.InfoContext(ctx, "embedded subtitle track demuxed from the source", "component", "playback",
			"file_id", opts.Embedded.MediaFileID, "stream_index", opts.Embedded.StreamIndex, "reason", err)
		return c.demuxAndKeep(ctx, opts, extract)
	}
}

// window reads an explicit window of opts' track through the cache's
// EmbeddedSubtitleSource, or returns nil when the source must be demuxed.
func (c *SubtitleCache) window(ctx context.Context, opts StreamExtractOpts) *SubtitlePacketInput {
	if c == nil || c.source == nil || opts.Embedded == nil {
		return nil
	}
	from, to := embeddedsub.WholeFrom, embeddedsub.WholeTo
	if opts.SeekSeconds > 0 {
		from = secondsDuration(opts.SeekSeconds)
	}
	if opts.DurationSeconds > 0 {
		to = secondsDuration(opts.SeekSeconds + opts.DurationSeconds)
	}
	in, err := c.source.Window(ctx, opts.InputPath, *opts.Embedded, from, to)
	if err != nil {
		if ctx.Err() == nil {
			slog.DebugContext(ctx, "subtitle window demuxed from the source", "component", "playback",
				"file_id", opts.Embedded.MediaFileID, "stream_index", opts.Embedded.StreamIndex, "reason", err)
		}
		return nil
	}
	return in
}

func secondsDuration(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

// serveWindowFromSource answers a window request from the cache's
// EmbeddedSubtitleSource. It reports false, having written nothing, when
// the caller should demux the source instead.
func (c *SubtitleCache) serveWindowFromSource(ctx context.Context, w io.Writer, opts StreamExtractOpts, extract SUPExtractFunc) (bool, error) {
	in := c.window(ctx, opts)
	if in == nil {
		return false, nil
	}
	wrote, err := convertPackets(ctx, w, opts, in, extract)
	if err != nil && !wrote && ctx.Err() == nil {
		slog.WarnContext(ctx, "converting a subtitle window failed; demuxing the source", "component", "playback",
			"file_id", opts.Embedded.MediaFileID, "stream_index", opts.Embedded.StreamIndex, "error", err)
		return false, nil
	}
	return true, err
}

// convertPackets converts in to w with extract. The output is held until
// it is complete or passes subtitleSpillBytes, so wrote reports false when
// a failed conversion left w untouched and the caller can still demux the
// source instead.
func convertPackets(ctx context.Context, w io.Writer, opts StreamExtractOpts, in *SubtitlePacketInput, extract SUPExtractFunc) (wrote bool, err error) {
	sw := &spillWriter{w: w, limit: subtitleSpillBytes}
	opts.Packets = in
	opts.Writer = sw
	opts.KeepPath, opts.KeepFormat, opts.KeepOffset = "", "", 0
	if err := extract(ctx, opts); err != nil {
		return sw.spilled, err
	}
	if _, format := streamExtractOutput(opts.SourceCodec, opts.TargetFormat); in.ExpectCues && !sw.spilled && !hasCue(format, sw.buf.Bytes()) {
		return false, errNoCueConverted
	}
	return true, sw.finish()
}

// errNoCueConverted is a conversion of a track with packets that yielded no
// cue: FFmpeg failed to read its input, though it exited cleanly.
var errNoCueConverted = errors.New("subtitle conversion yielded no cue from a track with packets")

// hasCue reports whether converted output in format holds a cue: a timing
// line with text after it for WebVTT and SubRip, an event line for ASS, any
// byte for SUP.
func hasCue(format string, data []byte) bool {
	switch format {
	case subtitleMuxerWebVTT, SubtitleFormatVTTV3, subtitleFormatSRT:
		return hasTimedText(data)
	case subtitleFormatASS:
		return bytes.Contains(data, []byte("\nDialogue:")) //nolint:misspell // the ASS event keyword
	}
	return len(data) > 0
}

// hasTimedText reports whether a WebVTT or SubRip document has a timing line
// followed by a line of text.
func hasTimedText(data []byte) bool {
	for len(data) > 0 {
		var line []byte
		line, data, _ = bytes.Cut(data, []byte{'\n'})
		if !bytes.Contains(line, []byte("-->")) {
			continue
		}
		if text, _, _ := bytes.Cut(data, []byte{'\n'}); len(bytes.TrimSpace(text)) > 0 {
			return true
		}
	}
	return false
}

// spillWriter holds writes until limit bytes, then passes everything on.
type spillWriter struct {
	w       io.Writer
	limit   int
	buf     bytes.Buffer
	spilled bool
}

func (s *spillWriter) Write(p []byte) (int, error) {
	if !s.spilled && s.buf.Len()+len(p) <= s.limit {
		return s.buf.Write(p)
	}
	if err := s.spill(); err != nil {
		return 0, err
	}
	return s.w.Write(p)
}

func (s *spillWriter) spill() error {
	if s.spilled {
		return nil
	}
	s.spilled = true
	_, err := s.w.Write(s.buf.Bytes())
	s.buf = bytes.Buffer{}
	return err
}

// Flush passes a flush on once the output streams.
func (s *spillWriter) Flush() {
	if f, ok := s.w.(http.Flusher); ok && s.spilled {
		f.Flush()
	}
}

// finish writes what is held and flushes it.
func (s *spillWriter) finish() error {
	if err := s.spill(); err != nil {
		return err
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// demuxAndKeep runs today's demux of the whole source. When the source can
// keep the track, FFmpeg also copies it into a file, which is kept once the
// demux has served its viewers. A demux that fails with the copy runs again
// without it, skipping what it already wrote: the copy must never cost a
// viewer the track.
func (c *SubtitleCache) demuxAndKeep(ctx context.Context, opts StreamExtractOpts, extract SUPExtractFunc) error {
	ref := *opts.Embedded
	dir := c.dir()
	if dir == "" {
		return extract(ctx, opts)
	}
	demuxCopy, err := c.source.DemuxCopy(ctx, opts.InputPath, ref)
	if err != nil {
		return extract(ctx, opts)
	}
	src, err := fspath.StatContext(ctx, opts.InputPath)
	if err != nil {
		return extract(ctx, opts)
	}
	// The fill running this demux created dir.
	tmp, err := os.CreateTemp(dir, "kept.part-*")
	if err != nil {
		return extract(ctx, opts)
	}
	copyPath := tmp.Name()
	_ = tmp.Close()
	demuxCopy.Path = copyPath
	counter := &countingWriter{w: opts.Writer}
	keeping := opts
	keeping.Writer = counter
	keeping.KeepPath, keeping.KeepFormat, keeping.KeepOffset = copyPath, demuxCopy.Format, demuxCopy.StartTime
	err = extract(ctx, keeping)
	if err != nil {
		_ = os.Remove(copyPath)
		if ctx.Err() != nil || errors.Is(err, errSubtitleWriter) {
			return err
		}
		slog.WarnContext(ctx, "subtitle demux with a kept copy failed; demuxing again without it", "component", "playback",
			"file_id", ref.MediaFileID, "stream_index", ref.StreamIndex, "error", err)
		opts.Writer = &skipWriter{w: opts.Writer, n: counter.n}
		return extract(ctx, opts)
	}
	// -fs stops only the kept output; the viewer still gets the whole
	// conversion. Its muxer buffer or trailer can cross the cutoff, so
	// a copy at or above it must never be recorded as a complete track.
	if info, err := os.Stat(copyPath); err != nil || info.Size() >= keeping.demuxCopyLimit() {
		_ = os.Remove(copyPath)
		slog.DebugContext(ctx, "demuxed subtitle copy not kept", "component", "playback",
			"file_id", ref.MediaFileID, "stream_index", ref.StreamIndex,
			"size_limit", keeping.demuxCopyLimit(), "error", err)
		return nil
	}
	go func() {
		defer func() { _ = os.Remove(copyPath) }()
		keepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keepDemuxedTimeout)
		defer cancel()
		if err := c.source.KeepDemuxed(keepCtx, opts.InputPath, ref, demuxCopy, src.Size(), src.ModTime()); err != nil {
			slog.WarnContext(keepCtx, "keeping a demuxed subtitle track failed", "component", "playback",
				"file_id", ref.MediaFileID, "stream_index", ref.StreamIndex, "error", err)
		}
	}()
	return nil
}

// errSubtitleWriter marks a failed write to the extract's writer, which a
// second demux would not cure.
var errSubtitleWriter = errors.New("subtitle output write failed")

// countingWriter counts the bytes written through it and marks its
// writer's failures with errSubtitleWriter.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err != nil {
		return n, errors.Join(errSubtitleWriter, err)
	}
	return n, nil
}

func (c *countingWriter) Flush() {
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

// ExtractSourcedText returns a whole embedded text track as "srt" or "ass"
// from the cache, or read through the cache's EmbeddedSubtitleSource, and
// caches it like ExtractText. ok is false, with a nil error, when only a
// demux of the source can produce it; the caller then extracts as before.
func (c *SubtitleCache) ExtractSourcedText(ctx context.Context, inputPath string, ref *EmbeddedSubtitleRef, ordinal int, format, ffmpegPath string) (data []byte, ok bool, err error) {
	if c == nil || c.source == nil || ref == nil {
		return nil, false, nil
	}
	data, err = c.ExtractText(ctx, inputPath, ordinal, format, func(ctx context.Context) ([]byte, error) {
		in, err := c.source.WholeTrack(ctx, inputPath, *ref)
		if err != nil {
			return nil, errors.Join(errNoSourcedTrack, err)
		}
		data, err := ExtractSubtitlePacketsWithFormat(ctx, in, format, ffmpegPath)
		if err == nil && in.ExpectCues && !hasCue(format, data) {
			err = errNoCueConverted
		}
		if err != nil {
			return nil, errors.Join(errNoSourcedTrack, err)
		}
		return data, nil
	})
	if errors.Is(err, errNoSourcedTrack) && ctx.Err() == nil {
		slog.DebugContext(ctx, "embedded subtitle track needs a demux", "component", "playback",
			"file_id", ref.MediaFileID, "stream_index", ref.StreamIndex, "reason", err)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

var errNoSourcedTrack = errors.New("embedded subtitle source can't read the track")

// WarmEmbeddedTextTracks is WarmTextTracks for a file whose tracks ref
// names: each rendition comes from a kept copy or a read through the
// file's index when it can, and only the rest from one batch demux of the
// source, in the same warm slot. Like WarmTextTracks it takes the slot
// without blocking.
func (c *SubtitleCache) WarmEmbeddedTextTracks(inputPath string, tracks []TextSubtitleTrack, ref func(ordinal int) *EmbeddedSubtitleRef, batch TextSubtitleBatchFunc, ffmpegPath string) {
	if c == nil || c.source == nil || ref == nil {
		c.WarmTextTracks(inputPath, tracks, batch)
		return
	}
	select {
	case c.warmSem <- struct{}{}:
	default:
		slog.Debug("subtitle text warm skipped: all warm slots busy", "input", inputPath)
		return
	}
	go func() {
		defer func() { <-c.warmSem }()
		ctx, cancel := context.WithTimeout(context.Background(), subtitleCacheWarmTimeout)
		defer cancel()
		var demux []TextSubtitleTrack
		for _, track := range tracks {
			if _, ok, err := c.ExtractSourcedText(ctx, inputPath, ref(track.Ordinal), track.Ordinal, track.Format, ffmpegPath); !ok || err != nil {
				demux = append(demux, track)
			}
		}
		if fills := c.beginTextFills(inputPath, demux); len(fills) > 0 && batch != nil {
			c.runTextWarm(inputPath, fills, batch)
		} else {
			discardTextFills(fills)
		}
	}()
}
