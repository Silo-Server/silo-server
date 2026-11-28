package mediasample

import (
	"context"
	"encoding/binary"
	"io"
	"math"
)

// An MP4 sample table lists its sync samples (keyframes) in an stss box,
// and one without that box makes every sample a sync sample. An stss that
// is present but lists no sample, as some muxers write, says none is: ffmpeg
// then indexes only the first video sample as a keyframe, unless an stps
// (partial sync samples) or a 'rap ' sample group marks others. The concat
// demuxer seeks every list entry of such a file to its start, so entry k
// decodes every keyframe up to its sample, and a long file's list runs for
// the square of its length and times out. The decoder still finds each
// keyframe in the stream itself, so such a file is read as one
// keyframes-only window, as containers the list cannot seek in are.

// mp4BoxLimit bounds the boxes the sync sample check visits, so a damaged or
// hostile box tree cannot keep it reading.
const mp4BoxLimit = 4096

// syncSampleCheck remembers, across a run's attempts, whether its MP4 input
// lists no sync samples, so the file is read once per run once a read
// finishes.
type syncSampleCheck struct {
	done, lacking bool
}

// lacks reports whether the MP4 file at path lists no sync samples (see
// lacksSyncSamples). The read runs like a Sheets input's Cues lookup (see
// boundedLookup) within ctx, the attempt's. One that finds no free slot or
// does not finish in time reports false, for the attempt to read its list as
// before, and the next attempt checks again.
func (c *syncSampleCheck) lacks(ctx context.Context, path string) bool {
	if !c.done {
		c.lacking, c.done = boundedLookup(ctx, func(ctx context.Context) bool { return lacksSyncSamples(ctx, path) })
	}
	return c.lacking
}

// lacksSyncSamples reports whether the MP4 file at path has a first video
// track whose sample table holds samples but marks none of them as a sync
// sample. It reads only box headers and the tables' counts, and reports
// false for any file it cannot read or does not understand.
func lacksSyncSamples(ctx context.Context, path string) bool {
	file, size, err := openRegularKeyframeSource(ctx, path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	w := &mp4Walker{r: contextReaderAt{ctx: ctx, r: file}}
	moov, ok := w.find(mp4Payload{end: size}, "moov")
	if !ok {
		return false
	}
	lacking := false
	w.each(moov, func(trak mp4Payload) bool {
		if trak.kind != "trak" {
			return true
		}
		mdia, ok := w.find(trak, "mdia")
		if !ok || w.handler(mdia) != "vide" {
			return true
		}
		if minf, ok := w.find(mdia, "minf"); ok {
			if stbl, ok := w.find(minf, "stbl"); ok {
				lacking = w.syncSamplesMissing(stbl)
			}
		}
		return false
	})
	return lacking && !w.failed
}

// mp4Payload is the payload span of one box.
type mp4Payload struct {
	kind       string
	start, end int64
}

// mp4Walker reads box headers, giving up, failed, on a read error or after
// mp4BoxLimit boxes.
type mp4Walker struct {
	r       io.ReaderAt
	visited int
	failed  bool
}

// each calls fn with each box in parent's payload, in order, until fn returns
// false or a box does not fit.
func (w *mp4Walker) each(parent mp4Payload, fn func(mp4Payload) bool) {
	end := parent.end
	for pos := parent.start; pos+8 <= end && !w.failed; {
		w.visited++
		if w.visited > mp4BoxLimit {
			w.failed = true
			return
		}
		var header [16]byte
		if _, err := w.r.ReadAt(header[:8], pos); err != nil {
			w.failed = true
			return
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		headerLen := int64(8)
		switch size {
		case 0:
			size = end - pos
		case 1:
			if _, err := w.r.ReadAt(header[8:16], pos+8); err != nil {
				w.failed = true
				return
			}
			large := binary.BigEndian.Uint64(header[8:16])
			if large > math.MaxInt64 {
				return
			}
			size = int64(large)
			headerLen = 16
		}
		if size < headerLen || size > end-pos {
			return
		}
		if !fn(mp4Payload{kind: string(header[4:8]), start: pos + headerLen, end: pos + size}) {
			return
		}
		pos += size
	}
}

// find returns the first box of kind in parent's payload.
func (w *mp4Walker) find(parent mp4Payload, kind string) (mp4Payload, bool) {
	var found mp4Payload
	ok := false
	w.each(parent, func(box mp4Payload) bool {
		found, ok = box, box.kind == kind
		return !ok
	})
	return found, ok
}

// uint32At reads the 32-bit value at offset within box, if it fits.
func (w *mp4Walker) uint32At(box mp4Payload, offset int64) (uint32, bool) {
	if box.start+offset+4 > box.end {
		return 0, false
	}
	var value [4]byte
	if _, err := w.r.ReadAt(value[:], box.start+offset); err != nil {
		w.failed = true
		return 0, false
	}
	return binary.BigEndian.Uint32(value[:]), true
}

// handler is the handler type of a media box: "vide" for video.
func (w *mp4Walker) handler(mdia mp4Payload) string {
	hdlr, ok := w.find(mdia, "hdlr")
	if !ok {
		return ""
	}
	kind, ok := w.uint32At(hdlr, 8)
	if !ok {
		return ""
	}
	return string(binary.BigEndian.AppendUint32(nil, kind))
}

// rapGrouping is the 'rap ' grouping type as an sbgp box stores it.
var rapGrouping = binary.BigEndian.Uint32([]byte("rap "))

// syncSamplesMissing reports whether a sample table holds samples and has an
// stss that lists none of them, with no stps or 'rap ' sample group that
// marks others (the tables ffmpeg also indexes keyframes from).
func (w *mp4Walker) syncSamplesMissing(stbl mp4Payload) bool {
	var stss, stps, samples uint32
	hasStss, hasSamples, rap := false, false, false
	w.each(stbl, func(box mp4Payload) bool {
		switch box.kind {
		case "stss":
			stss, hasStss = w.uint32At(box, 4)
		case "stps":
			stps, _ = w.uint32At(box, 4)
		case "sbgp":
			grouping, _ := w.uint32At(box, 4)
			rap = rap || grouping == rapGrouping
		case "stsz", "stz2":
			samples, hasSamples = w.uint32At(box, 8)
		}
		return true
	})
	return hasStss && stss == 0 && stps == 0 && !rap && hasSamples && samples > 1
}
