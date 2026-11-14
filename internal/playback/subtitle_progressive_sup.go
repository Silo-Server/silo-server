package playback

import (
	"encoding/binary"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// Progressive bitmap subtitles: one unbounded .sup extract per source, track and
// format generation, served behind a coverage watermark.
//
// A windowed PGS extract cannot seek a non-seekable relay, so ffmpeg demuxes the
// container from byte 0 and discards the prefix: the cost of a page at offset S
// is (S + W) × bitrate, not W × bitrate. A 120 s page of an 18.25 GB / 35 Mbps
// source is ~525 MB, which is also subtitleWindowExtractTimeout, so such pages
// died at the timeout and returned a partial .sup with no complete display set.
//
// The fix is to demux the track once, unbounded, and serve pages from what that
// extract has already written. The extract is the cache's ordinary full-track
// fill (one per key, single-flighted, in a warm slot drawing the detached relay
// budget); this file only adds what a page needs on top of it:
//
//   - supCoverage parses the artifact as it grows and records how far the
//     display sets it holds reach (coveredUntilSeconds), so a page can be
//     answered without reading the source;
//   - the fill serves a covered page by copying the display sets that show in
//     the window out of its own artifact file;
//   - a page beyond the watermark is retryable, advertising the covered range,
//     and the miss kicks the fill.
//
// The watermark never overstates. It advances only at a complete display set
// (PCS … END) that has been read in full, so a page ahead of it is never
// answered from a truncated run, and a fill that ends early publishes nothing.

const (
	// SubtitlePageNotReadyReason is the additive problem code for a bitmap page
	// whose start the track's progressive artifact has not parsed yet. It keeps
	// the retryable 503 shape subtitle_source_unavailable uses, but names the
	// real condition: the source is fine and the artifact that serves the page is
	// still filling, so the page will be answered without another source read.
	SubtitlePageNotReadyReason = "subtitle_page_not_ready"

	// subtitleProgressiveSUPMinSourceBytes is the source size at or above which
	// a bounded bitmap page is answered from the progressive artifact instead
	// of a source extract. It mirrors the API handler's implicit-window
	// threshold (virtualSubtitleImplicitWindowMinSourceBytes): below it, the
	// whole container a page has to read is cheap enough to extract directly.
	//
	// The size must be known to take this path. A row whose provider never
	// declared a size keeps today's page extract, which the read budget caps and
	// which is the safe answer when nothing is known about the container: no page
	// is refused for an assumption. A virtual row's size comes from the provider's
	// declared release size, so a multi-GB relay source normally has one.
	subtitleProgressiveSUPMinSourceBytes int64 = 256 << 20

	// supProgressiveMaxSets bounds the display-set index one artifact keeps in
	// memory. A feature-length track has a few thousand display sets; the cap
	// only exists so a pathological stream cannot grow the index without bound.
	// Past it the oldest half is dropped, which costs coverage of the dropped
	// prefix (a page there is answered retryable) and never overstates.
	supProgressiveMaxSets = 1 << 16

	// supMaxProgressiveTail is the largest partial segment a parser can hold:
	// a 13-byte header and a 16-bit payload size.
	supMaxProgressiveTail = supSegmentHeaderLen + 0xffff
)

// supDisplaySet is one display set in a .sup stream: the byte range it occupies
// in the artifact and the source-time range it covers. off starts at the
// boundary the previous set ended on, so segments that belong to no set (a
// palette definition written before its display set) travel with the set that
// uses them.
type supDisplaySet struct {
	off  int64
	end  int64
	from float64
	to   float64
}

// supCoverage indexes the display sets of a growing .sup artifact.
//
// feed is called with each chunk exactly as it is written to the artifact, in
// order, so the index only ever describes bytes that are on disk. parse holds
// the unterminated tail between chunks: a set is recorded only once its END
// segment is complete.
type supCoverage struct {
	sets []supDisplaySet
	// tail is the bytes after the last complete segment parsed so far.
	tail []byte
	// offset is the artifact offset tail starts at.
	offset int64
	// lastEnd is the artifact offset just after the last complete set.
	lastEnd int64
	// open marks a PCS seen without its END.
	open     bool
	setStart int64
	setFrom  float64
	// coveredUntil is the source time, in seconds, that the artifact has parsed
	// completely: the end of the last complete display set.
	coveredUntil float64
	// compacted marks an index that dropped its oldest sets at the cap. A
	// window that starts before the first retained set may be missing the set
	// that was on screen at its start, so it is refused rather than served
	// without it.
	compacted bool
	// invalid marks a stream that is not .sup framing, which no page may be
	// served from.
	invalid bool
}

// feed adds the next chunk of the artifact. The caller must have written the
// same bytes to the artifact before calling.
func (c *supCoverage) feed(p []byte) {
	if c.invalid || len(p) == 0 {
		return
	}
	if len(c.tail) == 0 {
		c.tail = append(c.tail[:0], p...)
	} else {
		c.tail = append(c.tail, p...)
	}
	c.parse()
}

// parse consumes every complete segment in the tail and stops at the first
// incomplete one, leaving it for the next chunk.
func (c *supCoverage) parse() {
	consumed := 0
	for len(c.tail)-consumed >= supSegmentHeaderLen {
		buf := c.tail[consumed:]
		if buf[0] != 'P' || buf[1] != 'G' {
			// Not a .sup stream: no page can be served from it, and claiming
			// any coverage would answer with bytes no decoder can read.
			c.invalid = true
			break
		}
		size := int(binary.BigEndian.Uint16(buf[11:13]))
		if len(buf) < supSegmentHeaderLen+size {
			break
		}
		kind := buf[10]
		pts := float64(binary.BigEndian.Uint32(buf[2:6])) / 90000
		segOff := c.offset + int64(consumed)
		switch kind {
		case supSegmentPCS:
			if !c.open {
				// Include the orphan segments that follow the previous set:
				// they are palette definitions for the set that opens here.
				c.setStart = c.lastEnd
				c.setFrom = pts
				c.open = true
			}
		case supSegmentEND:
			if c.open {
				c.appendSet(supDisplaySet{
					off:  c.setStart,
					end:  segOff + int64(supSegmentHeaderLen) + int64(size),
					from: c.setFrom,
					to:   pts,
				})
				c.open = false
			}
		}
		consumed += supSegmentHeaderLen + size
	}
	if consumed > 0 {
		c.offset += int64(consumed)
		c.tail = append(c.tail[:0], c.tail[consumed:]...)
	}
	if len(c.tail) > supMaxProgressiveTail {
		// A tail larger than the largest possible segment means the framing is
		// broken; nothing past this point can be trusted.
		c.invalid = true
	}
}

// appendSet records one complete display set and advances the watermark.
//
// Segment timestamps in a .sup are written in decode order, so a set's END PTS
// is at or after everything the artifact already holds. The watermark takes the
// maximum anyway: a stream that regresses cannot pull coverage backwards over
// bytes a page was already served, and a regressed set never advances it.
func (c *supCoverage) appendSet(set supDisplaySet) {
	if len(c.sets) >= supProgressiveMaxSets {
		drop := len(c.sets) / 2
		c.sets = append(c.sets[:0], c.sets[drop:]...)
		c.compacted = true
	}
	c.sets = append(c.sets, set)
	c.lastEnd = set.end
	if set.to > c.coveredUntil {
		c.coveredUntil = set.to
	}
}

// covered reports the source time, in seconds, that the artifact has parsed, and
// whether the index can answer a window at all.
func (c *supCoverage) covered() (float64, bool) {
	if c == nil || c.invalid {
		return 0, false
	}
	return c.coveredUntil, true
}

// window returns the byte range of the artifact that holds every display set
// showing at any time in [start, end). ok is false when the window is not fully
// covered yet, or when the index cannot answer for it; an empty range (off ==
// end) is a covered window in which nothing is displayed.
//
// The set on screen at start is included: a display set stays up until the next
// PCS replaces it, so a bitmap that started before the window is still the one
// the viewer sees at its start.
func (c *supCoverage) window(start, end float64) (off, endOff int64, ok bool) {
	if c == nil || c.invalid || len(c.sets) == 0 || end > c.coveredUntil {
		return 0, 0, false
	}
	if c.compacted && start < c.sets[0].from {
		// The index no longer holds every set before start, so the one on
		// screen at start may be missing.
		return 0, 0, false
	}
	// Index of the first set that starts at or after start; the set before it
	// is the one still on screen at start.
	next := len(c.sets)
	for i, set := range c.sets {
		if set.from >= start {
			next = i
			break
		}
	}
	first := next
	if first == len(c.sets) {
		first = len(c.sets) - 1
	} else if first > 0 {
		first--
	}
	last := -1
	for i := first; i < len(c.sets) && c.sets[i].from <= end; i++ {
		last = i
	}
	if last < 0 {
		// The artifact is covered but holds nothing that shows in the window.
		if c.sets[first].from > end {
			return 0, 0, true
		}
		return 0, 0, false
	}
	return c.sets[first].off, c.sets[last].end, true
}

// progressiveSUPWindow returns the bounded window a page request asks for.
func progressiveSUPWindow(opts StreamExtractOpts) (start, end float64, bounded bool) {
	windowed, start, end, openEnded := opts.WindowCoverage()
	if !windowed || openEnded {
		return 0, 0, false
	}
	return start, end, true
}

// progressiveSUPEligible reports whether a bounded bitmap window must be
// answered from the track's progressive artifact rather than a source extract.
// It applies to a remote identity (a relay input, which cannot seek) whose
// container size is known and too large for a page read to be worth attempting.
// An unknown size is not eligible: the page extract is still cheaper than
// answering a page the artifact may never reach.
func progressiveSUPEligible(opts StreamExtractOpts) bool {
	if !IsPGS(opts.SourceCodec) || opts.InputIsExtractedSup || opts.InputIsExtractedText != "" || opts.Packets != nil {
		return false
	}
	if strings.TrimSpace(opts.CacheIdentity) == "" {
		return false
	}
	return opts.SourceSizeBytes >= subtitleProgressiveSUPMinSourceBytes
}

// progressiveSUPArtifact returns the running bitmap fill for opts' track and how
// far its artifact has parsed. Both are zero when no fill is in flight, when it
// has ended, or when its artifact cannot be indexed.
func (c *SubtitleCache) progressiveSUPArtifact(opts StreamExtractOpts) (*SubtitleCacheFill, float64) {
	if c == nil {
		return nil, 0
	}
	key, ok := c.formatEntryKey(opts.InputPath, opts.CacheIdentity, opts.TrackIndex, subtitleFormatSUP)
	if !ok {
		return nil, 0
	}
	c.mu.Lock()
	fill := c.inflight[key]
	c.mu.Unlock()
	return fill.servable()
}

// servable reports the fill and its coverage while it is still running, or nil
// when it has ended (committed or discarded) or its artifact cannot be indexed.
func (f *SubtitleCacheFill) servable() (*SubtitleCacheFill, float64) {
	if f == nil {
		return nil, 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended || f.failed {
		return nil, 0
	}
	covered, ok := f.sup.covered()
	if !ok {
		return nil, 0
	}
	return f, covered
}

// serveWindow copies the display sets of the fill's artifact that show in
// [start, end) to w. It reports served=false, having written nothing, when the
// artifact cannot answer the window; the caller then takes its other paths.
//
// The copy runs on an independent handle and outside the fill's lock, so a large
// page never blocks the extract's writes or backpressures its stdout pipe. Only
// the page's first display set is held before the response sees a byte: the cut
// is taken from the index, which records complete display sets only, and a cut
// that nevertheless holds none is refused rather than answered with bytes no
// decoder can draw (supHasDisplaySet). Everything after that set streams.
func (f *SubtitleCacheFill) serveWindow(w io.Writer, start, end float64) (written int64, served bool, err error) {
	f.mu.Lock()
	if f.ended || f.failed || f.sup == nil {
		f.mu.Unlock()
		return 0, false, nil
	}
	off, endOff, ok := f.sup.window(start, end)
	path := f.tmp.Name()
	limit := f.written
	f.mu.Unlock()
	if !ok || endOff > limit {
		return 0, false, nil
	}
	if endOff <= off {
		return 0, true, nil
	}
	src, err := fspath.Open(path)
	if err != nil {
		return 0, false, nil
	}
	defer func() { _ = src.Close() }()
	if _, err := src.Seek(off, io.SeekStart); err != nil {
		return 0, false, nil
	}
	first, ok := readSUPDisplaySet(src, endOff-off)
	if !ok || !supHasDisplaySet(first) {
		return 0, false, nil
	}
	n, err := w.Write(first)
	written = int64(n)
	if err != nil {
		return written, true, err
	}
	if rest := endOff - off - int64(len(first)); rest > 0 {
		copied, copyErr := io.CopyN(w, src, rest)
		written += copied
		if copyErr != nil {
			return written, true, copyErr
		}
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return written, true, nil
}

// readSUPDisplaySet reads one display set — a PCS through the END that closes it,
// with any segment that precedes the PCS — from the artifact, and reports whether
// it read a complete one. atMost bounds the bytes it will collect, so a damaged
// stream cannot make it hold an unbounded run, and the caller's reader is left
// just after the set so the rest of the page streams straight out.
func readSUPDisplaySet(r io.Reader, atMost int64) ([]byte, bool) {
	var out []byte
	header := make([]byte, supSegmentHeaderLen)
	open := false
	for int64(len(out)) < atMost {
		if _, err := io.ReadFull(r, header); err != nil {
			return nil, false
		}
		if header[0] != 'P' || header[1] != 'G' {
			return nil, false
		}
		size := int(binary.BigEndian.Uint16(header[11:13]))
		segment := make([]byte, supSegmentHeaderLen+size)
		copy(segment, header)
		if _, err := io.ReadFull(r, segment[supSegmentHeaderLen:]); err != nil {
			return nil, false
		}
		out = append(out, segment...)
		switch header[10] {
		case supSegmentPCS:
			open = true
		case supSegmentEND:
			if open {
				return out, true
			}
		}
	}
	return nil, false
}

// serveProgressiveSUPPage answers a bounded bitmap window from the progressive
// artifact before any source read. It reports served=false when the caller must
// continue on its other paths, and returns a retryable error when a fill exists
// but has not parsed the window's start: the page is not ready yet, and reading
// the source for it is exactly the cost this path exists to avoid.
//
// A window whose start is parsed but whose end is still ahead of the watermark
// serves the parsed prefix. Every display set that shows in that prefix is
// complete on disk, and the response advertises exactly the range it covers, so
// a page costs no more than the bytes behind it — the alternative, refusing the
// whole page, left every request at a playhead inside the covered range without
// an answer.
func (c *SubtitleCache) serveProgressiveSUPPage(w http.ResponseWriter, r *http.Request, opts StreamExtractOpts, extract SUPExtractFunc) (bool, error) {
	start, end, bounded := progressiveSUPWindow(opts)
	// A track is answered from its artifact when a page read over its container
	// is not worth attempting: a known-large container, or one whose page prefix
	// is priced beyond the budget. The second half matters because a refusal
	// starts the fill, and the pages it publishes must then be served from it
	// even when the row never declared a size.
	if !bounded || (!progressiveSUPEligible(opts) && !c.pagePrefixBeyondBudget(opts)) {
		return false, nil
	}
	fill, covered := c.progressiveSUPArtifact(opts)
	if fill == nil && !opts.DisableBackgroundWarm {
		// Kick the fill: the reservation is single-flighted, so every later miss
		// finds the same one and adds nothing.
		c.WarmInBackground(opts, extract, subtitleTraceRequestID(r.Context()))
		fill, covered = c.progressiveSUPArtifact(opts)
	}
	if fill == nil {
		// Nothing is filling, and this request may not start one (a relay input
		// whose registration dies with the request is warmed by its handler
		// instead). Keep today's page extract, which the read budget caps; a
		// page too costly for it tells the handler which page is waiting.
		return false, nil
	}
	// The cut stops at the watermark: a display set the artifact has not read in
	// full must never travel with the page.
	cutEnd := end
	if cutEnd > covered {
		cutEnd = covered
	}
	if start >= covered {
		// The fill is running and has not parsed the page's own start yet. The
		// page is not failing; it is not ready, and the covered range says how
		// far it got.
		return false, &SubtitleExtractionRetryError{
			RetryAfter:   subtitleFillBusyRetryAfter,
			Reason:       SubtitlePageNotReadyReason,
			CoveredUntil: covered,
		}
	}
	// The headers go on before the copy: the first Write commits the status and
	// headers, so setting them after a non-empty page's copy would leave every
	// such page without them. A window is a position-dependent slice, so an
	// intermediary must not cache it. The coverage header repeats what the
	// handler already advertised for a whole page, and narrows it to the parsed
	// prefix when the page is only partly parsed.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	SetSubtitleCoverageRange(w.Header(), start, cutEnd)
	written, served, err := fill.serveWindow(w, start, cutEnd)
	if served {
		recordSubtitleServeSource(subtitleServePGSProgressive)
		slog.Log(r.Context(), slog.LevelInfo, "subtitle page served from the progressive artifact",
			"component", "playback",
			"track", opts.TrackIndex,
			"window_start_seconds", start,
			"window_end_seconds", end,
			"served_end_seconds", cutEnd,
			"covered_until_seconds", covered,
			"bytes", written)
		return true, err
	}
	// The fill ended between the lookup and the copy: the committed artifact (if
	// it landed) or a page extract answers instead. Put back the range the
	// request asked for, so a path that answers it does not advertise the cut.
	SetSubtitleCoverageRange(w.Header(), start, end)
	return false, nil
}
