package embeddedsub

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"time"
)

// Bounds on reading an MP4 index. The sample tables of a film's moov are a
// few megabytes at most; a text track has a few thousand samples.
const (
	maxMoovSize = 64 << 20
	// maxMP4Samples bounds the samples of all an index's text tracks
	// together, checked before each track's table is expanded: uniform
	// sample tables are cheap to declare and cost 24 bytes a sample.
	maxMP4Samples    = 1 << 20
	maxTopLevelBoxes = 64
	// maxMP4KeptBox bounds each box a track keeps (tkhd, edts, mdhd, hdlr,
	// the media header, dinf, stsd) and the movie header: real ones are a
	// few hundred bytes. maxMP4KeptBytes bounds what an index's tracks keep
	// in all. A track over either keeps nothing and is unsupported.
	maxMP4KeptBox   = 64 << 10
	maxMP4KeptBytes = 16 << 20
	// mp4ReadGap and mp4MaxRead bound merging samples into one read: text
	// samples are small and often sit next to each other.
	mp4ReadGap = 4 << 10
	mp4MaxRead = 1 << 20
)

// indexConstructionBytes bounds what index loads hold while they build an
// index, across readers, caches and both containers, before cache eviction
// can apply. A load past it fails with errIndexReadBusy, which callers retry.
// A blocked movie read keeps its reservation until the parser returns, even
// after its callers have given up.
var indexConstructionBytes = newConstructionBudget(256 << 20)

// newConstructionBudget returns a budget of maxBytes whose refusal is
// errIndexReadBusy: it is shared, so a load past it may fit later.
func newConstructionBudget(maxBytes int64) *budget {
	b := newBudget(maxBytes)
	b.full = errIndexReadBusy
	return b
}

const (
	// mp4BoxFtyp and mp4BoxMoov are the file type and movie boxes.
	mp4BoxFtyp = "ftyp"
	mp4BoxMoov = "moov"
	mp4BoxMvhd = "mvhd"
	mp4BoxTrak = "trak"
	// unsupportedKeptBoxes is Track.Unsupported for a track whose boxes are
	// over what an index keeps.
	unsupportedKeptBoxes = "MP4 track headers too large"
	// mp4KindText is QuickTime text: a handler and a sample entry type.
	mp4KindText = "text"
	// unsupportedEditList is Track.Unsupported for an edit list the
	// reader doesn't map.
	unsupportedEditList = "MP4 edit list"
)

// mp4Track is an MP4 text track's sample table, and the source boxes the
// one-track file WriteStream writes copies unchanged, so FFmpeg applies the
// same edit list and decoder setup.
type mp4Track struct {
	samples []mp4Sample
	// timescale is the media timescale (mdhd); mediaTime and delay map
	// decode times to presentation times by the edit list.
	timescale uint32
	mediaTime int64
	delay     time.Duration
	// Whole source boxes, headers included.
	ftyp, mvhd, tkhd, edts, mdhd, hdlr, mediaHeader, dinf, stsd []byte
}

type mp4Sample struct {
	offset int64
	// dts is the decode time in media ticks.
	dts   int64
	size  uint32
	delta uint32
}

// keep copies the boxes the track keeps out of the moov they were parsed
// from, charging them to keptLeft. Boxes over maxMP4KeptBox, or over what is
// left, aren't kept, and keep reports false.
func (m *mp4Track) keep(keptLeft *int) bool {
	boxes := []*[]byte{&m.tkhd, &m.edts, &m.mdhd, &m.hdlr, &m.mediaHeader, &m.dinf, &m.stsd}
	total := 0
	for _, box := range boxes {
		if len(*box) > maxMP4KeptBox {
			total = math.MaxInt
			break
		}
		total += len(*box)
	}
	if total > *keptLeft {
		for _, box := range boxes {
			*box = nil
		}
		return false
	}
	*keptLeft -= total
	for _, box := range boxes {
		*box = bytes.Clone(*box)
	}
	return true
}

// boxBytes is the size of the boxes the track keeps.
func (m *mp4Track) boxBytes() int {
	n := 0
	for _, box := range [][]byte{m.ftyp, m.mvhd, m.tkhd, m.edts, m.mdhd, m.hdlr, m.mediaHeader, m.dinf, m.stsd} {
		n += len(box)
	}
	return n
}

// pts is a sample's presentation time on the container's timeline.
func (m *mp4Track) pts(dts int64) time.Duration {
	return m.delay + ticksToDuration(dts-m.mediaTime, int64(m.timescale))
}

// ticksToDuration converts ticks of a timescale without overflowing.
func ticksToDuration(ticks, timescale int64) time.Duration {
	q, r := ticks/timescale, ticks%timescale
	return time.Duration(q)*time.Second + time.Duration(r*int64(time.Second)/timescale)
}

// walkBoxes calls fn for each box in b with the whole box and its payload,
// until fn returns false. It reports false when a box is malformed.
func walkBoxes(b []byte, fn func(kind string, box, payload []byte) bool) bool {
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b))
		kind := string(b[4:8])
		hl := uint64(8)
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return false
			}
			size, hl = binary.BigEndian.Uint64(b[8:]), 16
		}
		if size < hl || size > uint64(len(b)) {
			return false
		}
		if !fn(kind, b[:size], b[hl:size]) {
			return true
		}
		b = b[size:]
	}
	return len(b) == 0
}

// childBox returns the first child box of the kind, whole and its payload.
func childBox(b []byte, kind string) (box, payload []byte) {
	walkBoxes(b, func(k string, bx, p []byte) bool {
		if k == kind {
			box, payload = bx, p
			return false
		}
		return true
	})
	return box, payload
}

// isMP4 reports whether head starts with a box an MP4 or MOV file starts
// with.
func isMP4(head []byte) bool {
	if len(head) < 8 {
		return false
	}
	size := binary.BigEndian.Uint32(head)
	switch string(head[4:8]) {
	case mp4BoxFtyp, mp4BoxMoov, "mdat", "free", "skip", "wide", "pnot":
		return size == 1 || size == 0 || size >= 8
	}
	return false
}

// readMP4Index reads an MP4 or MOV file's text tracks from its moov box.
func readMP4Index(r io.ReaderAt, size int64, head []byte) (*Index, error) {
	b, reserved := indexConstructionBytes, 0
	defer func() { b.give(reserved) }()
	reserve := func(moovBytes int) error {
		// Cover the movie, every expanded 24-byte sample, and cloned boxes.
		// Double the clone allowance and leave 4 MiB for track/slice metadata
		// and allocation rounding. Small movies still allow eight loaders.
		n := moovBytes + maxMP4Samples*24 + 2*min(moovBytes, maxMP4KeptBytes) + (4 << 20)
		if err := b.take(n); err != nil {
			return errIndexReadBusy
		}
		reserved = n
		return nil
	}
	moov, ftyp, err := findMoov(r, size, head, reserve)
	if err != nil {
		return nil, err
	}
	var (
		mvhd       []byte
		traks      [][]byte
		fragmented bool
	)
	walkBoxes(moov, func(kind string, box, payload []byte) bool {
		switch kind {
		case mp4BoxMvhd:
			mvhd = box
		case "mvex":
			fragmented = true
		case mp4BoxTrak:
			traks = append(traks, payload)
			if len(traks) > maxTrackEntries {
				return false
			}
		}
		return true
	})
	movieTimescale, ok := mvhdTimescale(mvhd)
	if !ok {
		return nil, fmt.Errorf("%w: MP4 movie header missing or invalid", ErrMalformed)
	}
	if len(traks) > maxTrackEntries {
		return nil, fmt.Errorf("%w: MP4 movie has more than %d tracks", ErrMalformed, maxTrackEntries)
	}
	if len(mvhd) > maxMP4KeptBox {
		return nil, fmt.Errorf("%w: MP4 movie header of %d bytes", ErrMalformed, len(mvhd))
	}
	idx := &Index{Container: ContainerMP4, TimestampScale: 1}
	samplesLeft, keptLeft := maxMP4Samples, maxMP4KeptBytes
	// The boxes a track keeps are copied out of moov, which also holds
	// every other track's sample tables and would otherwise stay alive with
	// a cached index (mp4Track.keep).
	ftyp, mvhd = bytes.Clone(ftyp), bytes.Clone(mvhd)
	for _, trak := range traks {
		t, ok := parseMP4Track(trak, movieTimescale, size, &samplesLeft, &keptLeft)
		if samplesLeft < 0 {
			return nil, fmt.Errorf("%w: MP4 text tracks list more than %d samples", ErrMalformed, maxMP4Samples)
		}
		if !ok {
			continue
		}
		t.mp4.ftyp, t.mp4.mvhd = ftyp, mvhd
		if fragmented && t.Unsupported == "" {
			// Samples in movie fragments aren't in the sample table.
			t.Unsupported = "fragmented MP4"
		}
		idx.Tracks = append(idx.Tracks, t)
	}
	return idx, nil
}

// findMoov walks the top-level boxes, reading their headers, to the moov
// box, which it reads whole and returns the payload of. It also returns the
// ftyp box, if any. reserve admits construction before the movie is read.
func findMoov(r io.ReaderAt, size int64, head []byte, reserve func(int) error) (moov, ftyp []byte, err error) {
	for pos, n := int64(0), 0; pos+8 <= size && n < maxTopLevelBoxes; n++ {
		hdr := tail(head, pos)
		if len(hdr) < 16 {
			if hdr, err = readUpTo(r, pos, 16); err != nil {
				return nil, nil, fmt.Errorf("embeddedsub: read MP4 box at %d: %w", pos, err)
			}
		}
		if len(hdr) < 8 {
			break
		}
		boxSize, hl := int64(binary.BigEndian.Uint32(hdr)), int64(8)
		switch boxSize {
		case 0:
			boxSize = size - pos
		case 1:
			if len(hdr) < 16 {
				return nil, nil, fmt.Errorf("%w: MP4 box header cut off", ErrMalformed)
			}
			big := binary.BigEndian.Uint64(hdr[8:])
			if big > math.MaxInt64 {
				return nil, nil, fmt.Errorf("%w: MP4 box size out of range", ErrMalformed)
			}
			boxSize, hl = int64(big), 16
		}
		if boxSize < hl || boxSize > size-pos {
			return nil, nil, fmt.Errorf("%w: MP4 box at %d has bad size %d", ErrMalformed, pos, boxSize)
		}
		switch string(hdr[4:8]) {
		case mp4BoxFtyp:
			if boxSize <= 4096 {
				if ftyp, err = readBox(r, head, pos, boxSize); err != nil {
					return nil, nil, err
				}
			}
		case mp4BoxMoov:
			if boxSize > maxMoovSize {
				return nil, nil, fmt.Errorf("%w: MP4 moov of %d bytes is over the limit", ErrMalformed, boxSize)
			}
			if err := reserve(int(boxSize)); err != nil {
				return nil, nil, err
			}
			box, err := readBox(r, head, pos, boxSize)
			if err != nil {
				return nil, nil, err
			}
			return box[hl:], ftyp, nil
		}
		pos += boxSize
	}
	return nil, nil, fmt.Errorf("%w: no MP4 moov box", ErrMalformed)
}

// readBox returns the size bytes at pos, from head when it holds them.
func readBox(r io.ReaderAt, head []byte, pos, size int64) ([]byte, error) {
	if b := tail(head, pos); int64(len(b)) >= size {
		return b[:size], nil
	}
	b := make([]byte, size)
	if _, err := r.ReadAt(b, pos); err != nil {
		return nil, fmt.Errorf("embeddedsub: read MP4 box at %d: %w", pos, err)
	}
	return b, nil
}

func mvhdTimescale(mvhd []byte) (uint32, bool) {
	if len(mvhd) < 12 {
		return 0, false
	}
	p := mvhd[8:]
	off := 12 // after version 0's creation and modification times
	if p[0] == 1 {
		off = 20
	}
	if len(p) < off+4 {
		return 0, false
	}
	ts := binary.BigEndian.Uint32(p[off:])
	return ts, ts > 0
}

// stblBoxes are a sample table's boxes: stsd whole, the others' payloads.
type stblBoxes struct {
	stsd, stts, stsc, stsz, stco []byte
	co64, compositionOffsets     bool
}

// parseMP4Track reads a trak box; ok is false for a track that isn't a
// subtitle track.
// samplesLeft is what remains of the index's sample budget; a track that
// would exceed it sets it negative without expanding its table.
// keptLeft is what remains of the index's budget for kept boxes.
func parseMP4Track(trak []byte, movieTimescale uint32, fileSize int64, samplesLeft, keptLeft *int) (Track, bool) {
	tkhd, tkhdPayload := childBox(trak, "tkhd")
	edts, edtsPayload := childBox(trak, "edts")
	_, mdia := childBox(trak, "mdia")
	mdhd, mdhdPayload := childBox(mdia, "mdhd")
	hdlr, hdlrPayload := childBox(mdia, "hdlr")
	_, minf := childBox(mdia, "minf")
	if len(tkhdPayload) < 16 || len(hdlrPayload) < 12 || mdhd == nil || minf == nil {
		return Track{}, false
	}
	switch string(hdlrPayload[8:12]) {
	case "sbtl", mp4KindText, "subt":
	default:
		return Track{}, false
	}
	m := &mp4Track{tkhd: tkhd, edts: edts, mdhd: mdhd, hdlr: hdlr}
	t := Track{Language: "und", FrameCount: -1, Completeness: CompletenessNone, mp4: m}
	idOff := 12
	if tkhdPayload[0] == 1 {
		idOff = 20
	}
	if len(tkhdPayload) >= idOff+4 {
		t.Number = uint64(binary.BigEndian.Uint32(tkhdPayload[idOff:]))
	}
	t.Enabled = tkhdPayload[3]&1 != 0
	t.Default = t.Enabled
	var ok bool
	if m.timescale, t.Language, ok = parseMDHD(mdhdPayload); !ok {
		// Keep nothing of moov for a track nothing will read.
		m.tkhd, m.edts, m.mdhd, m.hdlr = nil, nil, nil, nil
		t.Unsupported = "MP4 media header"
		return t, true
	}
	var stbl stblBoxes
	walkBoxes(minf, func(kind string, box, payload []byte) bool {
		switch kind {
		case "nmhd", "sthd", "gmhd", "vmhd":
			if m.mediaHeader == nil {
				m.mediaHeader = box
			}
		case "dinf":
			m.dinf = box
		case "stbl":
			stbl = parseSTBL(payload)
		}
		return true
	})
	m.stsd = stbl.stsd
	if !m.keep(keptLeft) {
		t.Unsupported = unsupportedKeptBoxes
		return t, true
	}
	// The codec's private data is read from the kept stsd, not copied again.
	t.CodecID, t.CodecPrivate, t.Forced, t.Unsupported = sampleEntry(m.stsd)
	if t.Unsupported == "" && stbl.compositionOffsets {
		t.Unsupported = "MP4 composition offsets"
	}
	var editEnd time.Duration
	if t.Unsupported == "" {
		if m.mediaTime, m.delay, editEnd, ok = parseEditList(edtsPayload, movieTimescale); !ok {
			t.Unsupported = unsupportedEditList
		}
	}
	if t.Unsupported == "" {
		samples, err := expandSamples(stbl, fileSize, samplesLeft)
		if err != nil {
			t.Unsupported = err.Error()
		}
		m.samples = samples
	}
	// FFmpeg drops the samples that start at or after the media edit's
	// end, and keeps one that runs past it whole; so does the reader.
	if editEnd > 0 {
		m.samples = slices.DeleteFunc(m.samples, func(s mp4Sample) bool { return m.pts(s.dts) >= editEnd })
	}
	t.FrameCount = int64(len(m.samples))
	if len(m.samples) > 0 {
		// The sample table lists every sample.
		t.Completeness = CompletenessComplete
	}
	return t, true
}

// parseMDHD reads the media timescale and ISO 639-2 language.
func parseMDHD(p []byte) (timescale uint32, lang string, ok bool) {
	if len(p) < 4 {
		return 0, "", false
	}
	off, langOff := 12, 20 // version 0: 32-bit times and duration
	if p[0] == 1 {
		off, langOff = 20, 32
	}
	if len(p) < langOff+2 {
		return 0, "", false
	}
	timescale = binary.BigEndian.Uint32(p[off:])
	code := binary.BigEndian.Uint16(p[langOff:]) & 0x7FFF
	lang = "und"
	// Codes below 0x400 are Macintosh language codes, not ISO 639-2.
	if code >= 0x400 && code != 0x7FFF {
		lang = string([]byte{byte(code>>10&0x1F) + 0x60, byte(code>>5&0x1F) + 0x60, byte(code&0x1F) + 0x60})
	}
	return timescale, lang, timescale > 0
}

func parseSTBL(stbl []byte) stblBoxes {
	var s stblBoxes
	walkBoxes(stbl, func(kind string, box, payload []byte) bool {
		switch kind {
		case "stsd":
			s.stsd = box
		case "stts":
			s.stts = payload
		case "stsc":
			s.stsc = payload
		case "stsz":
			s.stsz = payload
		case "stco":
			s.stco = payload
		case "co64":
			s.stco, s.co64 = payload, true
		case "ctts":
			s.compositionOffsets = true
		}
		return true
	})
	return s
}

// sampleEntry reads the sample description: its type, the data after the
// SampleEntry header (a slice of stsd), and tx3g's "all samples forced"
// display flag. FFmpeg
// decodes both 3GPP timed text (tx3g) and QuickTime text (text) as
// mov_text; their samples are the same length-prefixed text.
func sampleEntry(stsd []byte) (kind string, private []byte, forced bool, unsupported string) {
	if len(stsd) < 16 {
		return "", nil, false, "MP4 sample description missing"
	}
	if binary.BigEndian.Uint32(stsd[12:]) != 1 {
		return "", nil, false, "several MP4 sample descriptions"
	}
	var entry []byte
	walkBoxes(stsd[16:], func(k string, _, payload []byte) bool {
		kind, entry = k, payload
		return false
	})
	if len(entry) < 8 {
		return kind, nil, false, "MP4 sample description missing"
	}
	private = entry[8:]
	switch kind {
	case "tx3g":
		forced = len(private) >= 4 && binary.BigEndian.Uint32(private)&0x80000000 != 0
	case mp4KindText:
	default:
		return kind, private, false, fmt.Sprintf("MP4 sample entry %q", kind)
	}
	return kind, private, forced, ""
}

// parseEditList reads the edit list as optional leading empty edits and one
// edit at normal rate, and where that edit ends on the presentation timeline
// (0 when it doesn't say). Anything else isn't mapped.
func parseEditList(edts []byte, movieTimescale uint32) (mediaTime int64, delay, end time.Duration, ok bool) {
	_, elst := childBox(edts, "elst")
	if elst == nil {
		return 0, 0, 0, true
	}
	if len(elst) < 8 {
		return 0, 0, 0, false
	}
	v1 := elst[0] == 1
	entrySize := 12
	if v1 {
		entrySize = 20
	}
	n := binary.BigEndian.Uint32(elst[4:])
	if int64(n) > int64((len(elst)-8)/entrySize) {
		return 0, 0, 0, false
	}
	seen := false
	for i := range int(n) {
		e := elst[8+i*entrySize:]
		var (
			duration uint64
			media    int64
			rate     uint32
		)
		if v1 {
			duration, media, rate = binary.BigEndian.Uint64(e), int64(binary.BigEndian.Uint64(e[8:])), binary.BigEndian.Uint32(e[16:]) //nolint:gosec // G115: media_time is signed
		} else {
			duration, media, rate = uint64(binary.BigEndian.Uint32(e)), int64(int32(binary.BigEndian.Uint32(e[4:]))), binary.BigEndian.Uint32(e[8:]) //nolint:gosec // G115: media_time is signed
		}
		switch {
		case media == -1 && !seen:
			if duration > math.MaxInt64 {
				return 0, 0, 0, false
			}
			delay += ticksToDuration(int64(duration), int64(movieTimescale))
		case media >= 0 && !seen && rate == 0x00010000:
			if duration > math.MaxInt64 {
				return 0, 0, 0, false
			}
			mediaTime, seen = media, true
			if duration > 0 {
				end = delay + ticksToDuration(int64(duration), int64(movieTimescale))
			}
		default:
			return 0, 0, 0, false
		}
	}
	return mediaTime, delay, end, true
}

// tableLen returns the entry count of a full box's table of entrySize-byte
// entries, reporting false when the payload doesn't hold them all.
func tableLen(p []byte, entrySize int) (int, bool) {
	if len(p) < 8 {
		return 0, false
	}
	n := binary.BigEndian.Uint32(p[4:])
	if int64(n)*int64(entrySize) > int64(len(p)-8) {
		return 0, false
	}
	return int(n), true
}

// expandSamples lists every sample's offset, size and decode time from the
// sample table.
func expandSamples(s stblBoxes, fileSize int64, samplesLeft *int) ([]mp4Sample, error) {
	if len(s.stsz) < 12 || s.stts == nil || s.stsc == nil || s.stco == nil {
		return nil, errors.New("MP4 sample table incomplete")
	}
	uniform := binary.BigEndian.Uint32(s.stsz[4:])
	count := int(binary.BigEndian.Uint32(s.stsz[8:]))
	if *samplesLeft -= count; *samplesLeft < 0 {
		return nil, fmt.Errorf("MP4 track with %d samples", count)
	}
	if uniform == 0 && count*4 > len(s.stsz)-12 {
		return nil, errors.New("MP4 sample sizes cut off")
	}
	chunkSize := 4
	if s.co64 {
		chunkSize = 8
	}
	chunks, ok := tableLen(s.stco, chunkSize)
	if !ok {
		return nil, errors.New("MP4 chunk offsets cut off")
	}
	runs, ok := tableLen(s.stsc, 12)
	if !ok || (runs == 0 && count > 0) {
		return nil, errors.New("MP4 sample-to-chunk table cut off")
	}
	times, ok := tableLen(s.stts, 8)
	if !ok {
		return nil, errors.New("MP4 time-to-sample table cut off")
	}

	samples := make([]mp4Sample, 0, count)
	for chunk, run := 0, 0; chunk < chunks && len(samples) < count; chunk++ {
		for run+1 < runs && int(binary.BigEndian.Uint32(s.stsc[8+(run+1)*12:])) <= chunk+1 {
			run++
		}
		perChunk := binary.BigEndian.Uint32(s.stsc[8+run*12+4:])
		off, err := chunkOffset(s, chunk)
		if err != nil {
			return nil, err
		}
		for range perChunk {
			if len(samples) == count {
				break
			}
			size := uniform
			if size == 0 {
				size = binary.BigEndian.Uint32(s.stsz[12+4*len(samples):])
			}
			if size > maxPacketSize {
				return nil, fmt.Errorf("MP4 sample of %d bytes", size)
			}
			if off > fileSize-int64(size) {
				return nil, errors.New("MP4 sample past the end of the file")
			}
			samples = append(samples, mp4Sample{offset: off, size: size})
			off += int64(size)
		}
	}
	if len(samples) != count {
		return nil, errors.New("MP4 chunks hold fewer samples than the table lists")
	}
	var dts int64
	i := 0
	for e := range times {
		n := binary.BigEndian.Uint32(s.stts[8+e*8:])
		delta := binary.BigEndian.Uint32(s.stts[8+e*8+4:])
		for ; n > 0 && i < len(samples); n-- {
			samples[i].dts, samples[i].delta = dts, delta
			dts += int64(delta)
			i++
		}
	}
	if i != len(samples) {
		return nil, errors.New("MP4 time-to-sample table covers fewer samples than the sizes")
	}
	return samples, nil
}

func chunkOffset(s stblBoxes, chunk int) (int64, error) {
	if !s.co64 {
		return int64(binary.BigEndian.Uint32(s.stco[8+chunk*4:])), nil
	}
	o := binary.BigEndian.Uint64(s.stco[8+chunk*8:])
	if o > math.MaxInt64 {
		return 0, errors.New("MP4 chunk offset out of range")
	}
	return int64(o), nil
}

// mp4Read is one read of neighboring samples.
type mp4Read struct {
	start, end int64
	samples    []int
}

// readMP4Packets reads the samples that show in [from, to), merging
// neighbors into one read.
func readMP4Packets(ctx context.Context, r *fileReader, b *budget, t *Track, from, to time.Duration, concurrency int) (*Result, error) {
	m := t.mp4
	whole := from == WholeFrom && to == WholeTo
	var sel []int
	for i, s := range m.samples {
		pts := m.pts(s.dts)
		if whole || overlaps(pts, pts+ticksToDuration(int64(s.delta), int64(m.timescale)), from, to) {
			if err := b.take(cueMetadataBytes); err != nil {
				return nil, err
			}
			sel = append(sel, i)
		}
	}
	slices.SortFunc(sel, func(a, b int) int { return cmpInt(m.samples[a].offset, m.samples[b].offset) })
	var reads []mp4Read
	for _, i := range sel {
		s := m.samples[i]
		end := s.offset + int64(s.size)
		if n := len(reads); n > 0 && s.offset <= reads[n-1].end+mp4ReadGap && end-reads[n-1].start <= mp4MaxRead {
			reads[n-1].end = max(reads[n-1].end, end)
			reads[n-1].samples = append(reads[n-1].samples, i)
			continue
		}
		reads = append(reads, mp4Read{start: s.offset, end: end, samples: []int{i}})
	}
	results, err := runJobs(ctx, len(reads), concurrency, func(j int) jobResult {
		return m.readSamples(r, b, reads[j])
	})
	if err != nil {
		return nil, err
	}
	res := &Result{Stats: ReadStats{Cues: len(sel)}}
	var damage []error
	for _, jr := range results {
		if jr.err != nil {
			return nil, jr.err
		}
		damage = append(damage, jr.damage...)
		res.Packets = append(res.Packets, jr.packets...)
	}
	// Samples are in decode order in the table, which FFmpeg demuxes in.
	slices.SortFunc(res.Packets, func(a, b Packet) int { return cmpInt(a.mp4DTS, b.mp4DTS) })
	if len(damage) > 0 {
		return res, &DamagedError{Cues: len(damage), First: damage[0]}
	}
	return res, nil
}

func (m *mp4Track) readSamples(r io.ReaderAt, b *budget, rd mp4Read) jobResult {
	n := int(rd.end - rd.start)
	if err := b.take(n); err != nil {
		return jobResult{err: err}
	}
	defer b.give(n)
	buf, err := readUpTo(r, rd.start, n)
	if err != nil {
		return jobResult{err: err}
	}
	var jr jobResult
	for _, i := range rd.samples {
		s := m.samples[i]
		lo, hi := s.offset-rd.start, s.offset-rd.start+int64(s.size)
		if hi > int64(len(buf)) {
			jr.damage = append(jr.damage, corrupt("MP4 sample at %d is cut off", s.offset))
			continue
		}
		if err := b.take(int(s.size) + packetMetadataBytes); err != nil {
			return jobResult{err: err}
		}
		jr.packets = append(jr.packets, Packet{
			PTS: m.pts(s.dts), Duration: ticksToDuration(int64(s.delta), int64(m.timescale)),
			Data: bytes.Clone(buf[lo:hi]), pos: s.offset, mp4DTS: s.dts, mp4Delta: s.delta,
		})
	}
	return jr
}
