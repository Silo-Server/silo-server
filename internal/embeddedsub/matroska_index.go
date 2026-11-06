package embeddedsub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// Bounds on what reading an index costs. The head read covers the EBML
// header, the seek head, Info and Tracks of nearly every file in one read.
// The size limits are far above real files (the largest Cues in a library
// survey was 1.07 MB) and keep a damaged file from allocating much.
const (
	headReadSize      = 64 << 10
	elementProbeSize  = 16 << 10
	maxEBMLHeaderSize = 4 << 10
	maxSeekHeadSize   = 1 << 20
	maxInfoSize       = 1 << 20
	maxTracksSize     = 16 << 20
	maxTagsSize       = 8 << 20
	maxCuesSize       = 16 << 20
	// maxMetadataBytes bounds the Info, Tracks, Cues and Tags one index
	// read holds at once; each has its own limit above as well.
	maxMetadataBytes   = 32 << 20
	maxSeekHeads       = 8
	maxTagsElements    = 4
	maxTopLevelVisited = 64
	// maxTrackEntries bounds the TrackEntries of a Tracks element, as
	// mediaprobe does; a crafted element of tiny entries would otherwise
	// make subtitleTracks allocate far more than the element's size.
	maxTrackEntries = 1000
	// maxIndexCues bounds the cue entries an index keeps across its
	// subtitle tracks, 32 bytes each. Real files have a few thousand a
	// track; a crafted Cues element of tiny entries would otherwise expand
	// to four times its size.
	maxIndexCues = 1 << 20
)

const docTypeMatroska = "matroska"

// errBadElement marks an element that isn't where, or what, the file says:
// a structural problem, unlike a failed read. An optional one is skipped.
var errBadElement = fmt.Errorf("%w: bad element", ErrMalformed)

// ReadIndex reads the subtitle tracks of a Matroska, MP4 or MOV file and
// where their packets are. For Matroska it reads the EBML header, the seek
// heads, Info, Tracks, Tags and Cues, never a cluster; for MP4 the moov box.
// A file without Cues still indexes, with every track's Completeness
// CompletenessNone. A file of another kind returns ErrNotSupportedContainer;
// a failed read returns the read's error.
func ReadIndex(ctx context.Context, path string) (*Index, error) {
	start := time.Now()
	f, info, err := openFile(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := newFileReader(ctx, f, 0)
	idx, err := readIndex(r, info.Size())
	if err != nil {
		return nil, err
	}
	idx.Size, idx.ModTime = info.Size(), info.ModTime()
	idx.Stats = r.stats()
	idx.Stats.Wall = time.Since(start)
	return idx, nil
}

func readIndex(r io.ReaderAt, size int64) (*Index, error) {
	head, err := readUpTo(r, 0, int(min(headReadSize, size)))
	if err != nil {
		return nil, fmt.Errorf("embeddedsub: read file head: %w", err)
	}
	if id, _, _, ok := matroska.ParseElementHeader(head); ok && id == matroska.IDEBML {
		return readMatroskaIndex(r, size, head)
	}
	if isMP4(head) {
		return readMP4Index(r, size, head)
	}
	return nil, ErrNotSupportedContainer
}

// segmentLayout is where a segment's top-level elements are, as file
// offsets.
type segmentLayout struct {
	start, end   int64
	info, tracks int64
	cues         int64
	tags         []int64
}

func readMatroskaIndex(r io.ReaderAt, size int64, head []byte) (*Index, error) {
	layout, err := segmentStart(head, size)
	if err != nil {
		return nil, err
	}
	if err := locateElements(r, head, &layout); err != nil {
		return nil, err
	}
	if layout.info < 0 || layout.tracks < 0 {
		return nil, fmt.Errorf("%w: Matroska Info or Tracks not found", ErrMalformed)
	}
	// What this load builds draws on the process's construction budget
	// (indexConstructionBytes) until it returns.
	held := newBudgetWithin(maxMetadataBytes, indexConstructionBytes)
	defer held.release()
	el, err := readSegmentElements(r, head, layout, held)
	if err != nil {
		return nil, err
	}
	scale, ok := parseTimestampScale(el.info)
	if !ok {
		return nil, fmt.Errorf("%w: Matroska Info has an invalid TimestampScale", ErrMalformed)
	}
	idx := &Index{Container: ContainerMatroska, TimestampScale: scale, segmentEnd: layout.end}
	privateBudget := newBudgetWithin(maxTracksSize, indexConstructionBytes)
	defer privateBudget.release()
	if idx.Tracks, err = subtitleTracks(el.tracks, privateBudget); err != nil {
		return nil, err
	}
	frames := make(map[uint64]int64, len(idx.Tracks))
	for _, t := range idx.Tracks {
		if t.UID != 0 {
			frames[t.UID] = -1
		}
	}
	for _, tags := range el.tags {
		parseFrameCounts(tags, frames)
	}
	if el.cues != nil {
		cueBudget := newBudgetWithin(int64(maxIndexCues)*int64(unsafe.Sizeof(Cue{})), indexConstructionBytes)
		defer cueBudget.release()
		ok, err := collectCues(el.cues, idx.Tracks, layout.start, scale, cueBudget)
		if err != nil {
			return nil, err
		}
		if !ok {
			// A malformed Cues element could hide entries; don't trust any.
			for i := range idx.Tracks {
				idx.Tracks[i].Cues = nil
			}
		}
	}
	for i := range idx.Tracks {
		t := &idx.Tracks[i]
		if n, ok := frames[t.UID]; ok && t.UID != 0 {
			t.FrameCount = n
		}
		t.Completeness = completeness(len(t.Cues), t.FrameCount)
	}
	return idx, nil
}

// segmentStart checks the EBML header in head and returns where the
// segment's data starts and ends.
func segmentStart(head []byte, size int64) (segmentLayout, error) {
	_, hdrSize, hl, _ := matroska.ParseElementHeader(head)
	if hdrSize < 0 || hdrSize > maxEBMLHeaderSize || int64(hl)+hdrSize > int64(len(head)) {
		return segmentLayout{}, ErrNotSupportedContainer
	}
	docType := docTypeMatroska // the EBML default
	matroska.Children(head[hl:int64(hl)+hdrSize], func(id uint64, v []byte) {
		if id == matroska.IDDocType {
			docType = matroska.String(v)
		}
	})
	if docType != docTypeMatroska && docType != "webm" {
		return segmentLayout{}, ErrNotSupportedContainer
	}
	pos := int64(hl) + hdrSize
	id, segSize, n, ok := matroska.ParseElementHeader(tail(head, pos))
	if !ok {
		return segmentLayout{}, fmt.Errorf("%w: no Matroska segment header at %d", ErrMalformed, pos)
	}
	if id != matroska.IDSegment {
		return segmentLayout{}, fmt.Errorf("%w: element %#x where the Matroska segment should be", ErrMalformed, id)
	}
	layout := segmentLayout{start: pos + int64(n), end: size, info: -1, tracks: -1, cues: -1}
	if segSize != matroska.UnknownSize && layout.start+segSize < size {
		layout.end = layout.start + segSize
	}
	return layout, nil
}

// segmentElements holds the data of the elements an index reads.
type segmentElements struct {
	info, tracks, cues []byte
	tags               [][]byte
}

// readSegmentElements reads Info, Tracks, Cues and Tags. They are
// independent, so the ones outside the head are read at once: Cues and Tags
// are usually at the end of the file. Cues and Tags are optional, so a
// misplaced or oversized one only costs the index; a failed read is
// reported.
func readSegmentElements(r io.ReaderAt, head []byte, layout segmentLayout, held *budget) (segmentElements, error) {
	reads := []*elementRead{
		{at: layout.info, want: matroska.IDInfo, limit: maxInfoSize},
		{at: layout.tracks, want: matroska.IDTracks, limit: maxTracksSize},
	}
	if layout.cues >= 0 {
		reads = append(reads, &elementRead{at: layout.cues, want: matroska.IDCues, limit: maxCuesSize})
	}
	for _, at := range layout.tags {
		reads = append(reads, &elementRead{at: at, want: matroska.IDTags, limit: maxTagsSize})
	}
	var wg sync.WaitGroup
	for _, er := range reads {
		wg.Go(func() { er.data, er.err = readTopElement(r, head, er.at, er.want, er.limit, held) })
	}
	wg.Wait()
	var el segmentElements
	for i, er := range reads {
		optional := i >= 2
		if er.err != nil {
			if optional && errors.Is(er.err, errBadElement) {
				continue
			}
			return segmentElements{}, er.err
		}
		switch er.want {
		case matroska.IDInfo:
			el.info = er.data
		case matroska.IDTracks:
			el.tracks = er.data
		case matroska.IDCues:
			el.cues = er.data
		default:
			el.tags = append(el.tags, er.data)
		}
	}
	return el, nil
}

func isShortRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func completeness(cues int, frames int64) Completeness {
	switch {
	case cues == 0:
		return CompletenessNone
	case frames < 0:
		return CompletenessUnknown
	case int64(cues) == frames:
		return CompletenessComplete
	default:
		return CompletenessIncomplete
	}
}

// locateElements finds the segment's Info, Tracks, Cues and Tags: from the
// top-level elements ahead of the first cluster and from the seek heads,
// following a seek head that points at another. It never walks past a
// cluster, so Cues stored after the clusters without a seek head entry
// aren't found.
func locateElements(r io.ReaderAt, head []byte, l *segmentLayout) error {
	var seekHeads []int64
	record := func(id uint64, at int64) {
		if at < l.start || at >= l.end {
			return
		}
		switch id {
		case matroska.IDInfo:
			if l.info < 0 {
				l.info = at
			}
		case matroska.IDTracks:
			if l.tracks < 0 {
				l.tracks = at
			}
		case matroska.IDCues:
			if l.cues < 0 {
				l.cues = at
			}
		case matroska.IDTags:
			if !slices.Contains(l.tags, at) && len(l.tags) < maxTagsElements {
				l.tags = append(l.tags, at)
			}
		case matroska.IDSeekHead:
			if !slices.Contains(seekHeads, at) && len(seekHeads) < maxSeekHeads {
				seekHeads = append(seekHeads, at)
			}
		}
	}

	// The top-level elements ahead of the first cluster, reading headers
	// past the head only while Info or Tracks are still missing.
	for at, visited := l.start, 0; at < l.end && visited < maxTopLevelVisited; visited++ {
		id, dataSize, hl, ok, err := topLevelHeader(r, head, at, l.info < 0 || l.tracks < 0)
		if err != nil {
			return err
		}
		if !ok || id == matroska.IDCluster || dataSize == matroska.UnknownSize {
			break
		}
		record(id, at)
		at += hl + dataSize
	}

	for i := 0; i < len(seekHeads); i++ {
		data, err := readTopElement(r, head, seekHeads[i], matroska.IDSeekHead, maxSeekHeadSize, nil)
		if err != nil {
			if errors.Is(err, errBadElement) {
				// A damaged seek head only loses what it points at.
				continue
			}
			return err
		}
		matroska.WalkSeekEntries(data, func(e matroska.SeekEntry) {
			if e.Position <= math.MaxInt64-l.start {
				record(e.ID, l.start+e.Position)
			}
		})
	}
	return nil
}

// topLevelHeader returns the header of the element at at: from head, or,
// when read is set and at is past what head holds, from the file. ok is
// false when there is none to use.
func topLevelHeader(r io.ReaderAt, head []byte, at int64, read bool) (id uint64, size, hl int64, ok bool, err error) {
	if hid, hsize, n, ok := matroska.ParseElementHeader(tail(head, at)); ok {
		return hid, hsize, int64(n), true, nil
	}
	if !read || at < int64(len(head))-matroska.MaxHeaderLen {
		// Malformed inside the head, or nothing more is needed.
		return 0, 0, 0, false, nil
	}
	id, size, hl, err = matroska.ReadElementHeader(r, at)
	if err != nil {
		if isShortRead(err) {
			return 0, 0, 0, false, nil
		}
		return 0, 0, 0, false, fmt.Errorf("embeddedsub: read Matroska element at %d: %w", at, err)
	}
	return id, size, hl, true, nil
}

// tail returns head from off on, or nil when off is outside it.
func tail(head []byte, off int64) []byte {
	if off < 0 || off >= int64(len(head)) {
		return nil
	}
	return head[off:]
}

type elementRead struct {
	at    int64
	want  uint64
	limit int64
	data  []byte
	err   error
}

// readTopElement returns the data of the element at at, which must be a
// want element of at most limit bytes: from head when it holds it all,
// otherwise in one read, or two for an element larger than a probe. held,
// when set, is charged for the bytes it allocates.
func readTopElement(r io.ReaderAt, head []byte, at int64, want uint64, limit int64, held *budget) ([]byte, error) {
	first := tail(head, at)
	id, size, hl, ok := matroska.ParseElementHeader(first)
	if !ok || size < 0 || int64(hl)+size > int64(len(first)) {
		var err error
		first, err = readUpTo(r, at, elementProbeSize)
		if err != nil {
			return nil, fmt.Errorf("embeddedsub: read Matroska element at %d: %w", at, err)
		}
		id, size, hl, ok = matroska.ParseElementHeader(first)
		if !ok {
			return nil, fmt.Errorf("%w: no element header at %d", errBadElement, at)
		}
	}
	if id != want {
		return nil, fmt.Errorf("%w: element at %d is %#x, not %#x", errBadElement, at, id, want)
	}
	if size < 0 || size > limit {
		return nil, fmt.Errorf("%w: element %#x at %d has unsupported size %d", errBadElement, want, at, size)
	}
	if int64(hl)+size <= int64(len(first)) {
		return first[hl : int64(hl)+size], nil
	}
	if held != nil {
		if err := held.take(int(size)); errors.Is(err, errIndexReadBusy) {
			return nil, err
		} else if err != nil {
			return nil, fmt.Errorf("%w: Matroska metadata over %d bytes", ErrMalformed, maxMetadataBytes)
		}
	}
	data := make([]byte, size)
	n := copy(data, first[hl:])
	if _, err := r.ReadAt(data[n:], at+int64(hl)+int64(n)); err != nil {
		if isShortRead(err) {
			return nil, fmt.Errorf("%w: element %#x at %d is cut off", errBadElement, want, at)
		}
		return nil, fmt.Errorf("embeddedsub: read Matroska element at %d: %w", at, err)
	}
	return data, nil
}

// parseTimestampScale reads Info's TimestampScale, defaulting when absent.
// An explicit zero, or one that malformed data might have hidden, isn't
// usable: every timestamp depends on it.
func parseTimestampScale(info []byte) (int64, bool) {
	scale, ok := int64(matroska.DefaultTimestampScale), true
	complete := matroska.ChildrenComplete(info, func(id uint64, v []byte) {
		if id != matroska.IDTimestampScale {
			return
		}
		if s := matroska.Uint(v); s > 0 && s <= math.MaxInt32 {
			scale = int64(s)
		} else {
			ok = false
		}
	})
	return scale, ok && complete
}

// isSubtitleTrack reports whether FFmpeg makes a subtitle stream of the
// entry: a subtitle track, or a metadata track of a D_ codec (WebVTT in
// WebM), with an S_ or D_ codec.
func isSubtitleTrack(e matroska.TrackEntry) bool {
	if e.Number == 0 || len(e.CodecID) < 2 {
		return false
	}
	switch e.Type {
	case matroska.TrackTypeSubtitle:
		return e.CodecID[0] == 'S' || e.CodecID[0] == 'D'
	case matroska.TrackTypeMetadata:
		return e.CodecID[0] == 'D'
	}
	return false
}

// subtitleTracks returns the subtitle TrackEntries of a Tracks element.
// Compressed codec private data can be much larger than Tracks, so
// privateBudget bounds its expansion across every track, including temporary
// growth buffers.
func subtitleTracks(data []byte, privateBudget *budget) ([]Track, error) {
	var (
		tracks  []Track
		entries int
		busy    error
	)
	matroska.Children(data, func(id uint64, v []byte) {
		if id != matroska.IDTrackEntry || busy != nil {
			return
		}
		if entries++; entries > maxTrackEntries {
			return
		}
		if e := matroska.ParseTrackEntry(v); isSubtitleTrack(e) {
			t, err := newTrack(e, privateBudget)
			tracks, busy = append(tracks, t), err
		}
	})
	if busy != nil {
		return nil, busy
	}
	if entries > maxTrackEntries {
		return nil, fmt.Errorf("%w: Matroska Tracks has more than %d entries", ErrMalformed, maxTrackEntries)
	}
	return tracks, nil
}

// newTrack returns e as a Track. Its error is only errIndexReadBusy, when
// the process's construction budget can't cover its codec private data now;
// a track the index can't use otherwise is Unsupported.
func newTrack(e matroska.TrackEntry, privateBudget *budget) (Track, error) {
	t := Track{
		Number: e.Number, UID: e.UID, CodecID: e.CodecID,
		Language: e.Language, LanguageBCP47: e.LanguageBCP47, Name: e.Name,
		Enabled: e.Enabled, Default: e.Default, Forced: e.Forced, HearingImpaired: e.HearingImpaired,
		FrameCount: -1, trackType: e.Type,
		CodecPrivate: bytes.Clone(e.CodecPrivate),
	}
	if e.DefaultDuration <= math.MaxInt64 {
		t.DefaultDuration = time.Duration(e.DefaultDuration)
	}
	if e.TimestampScale != 1 {
		// FFmpeg scales the track's timestamps again by it.
		t.Unsupported = "TrackTimestampScale"
	}
	if e.CodecDelay != 0 {
		t.Unsupported = "CodecDelay"
	}
	// FFmpeg applies a single ContentEncoding and ignores a chain of them.
	switch len(e.Encodings) {
	case 0:
	case 1:
		enc := e.Encodings[0]
		switch {
		case enc.Type != matroska.ContentEncodingCompression:
			t.Unsupported = "encrypted"
		case enc.CompAlgo != matroska.CompressionZlib && enc.CompAlgo != matroska.CompressionBzlib &&
			enc.CompAlgo != matroska.CompressionHeaderStripping:
			t.Unsupported = fmt.Sprintf("compression algorithm %d", enc.CompAlgo)
		default:
			c := &Compression{Algorithm: enc.CompAlgo, Settings: bytes.Clone(enc.CompSettings)}
			if enc.Scope&matroska.ScopeFrames != 0 {
				t.Compression = c
			}
			if enc.Scope&matroska.ScopeCodecPrivate != 0 && len(t.CodecPrivate) > 0 {
				if err := decompressPrivate(&t, c, privateBudget); err != nil {
					return Track{}, err
				}
			}
		}
	default:
		t.Unsupported = "multiple content encodings"
	}
	return t, nil
}

// decompressPrivate undoes c on t's codec private data within privateBudget,
// marking t Unsupported when it can't. Its error is only errIndexReadBusy.
func decompressPrivate(t *Track, c *Compression, privateBudget *budget) error {
	private, err := decompressBudget(c, t.CodecPrivate, privateBudget)
	switch {
	case errors.Is(err, errIndexReadBusy):
		return err
	case errors.Is(err, ErrTooLarge):
		t.Unsupported = "codec private data exceeds index budget"
	case err != nil:
		t.Unsupported = "codec private data doesn't decompress"
	}
	t.CodecPrivate = private
	return nil
}

// parseFrameCounts updates NUMBER_OF_FRAMES for the TrackUIDs already in
// counts. Its caller seeds only indexed subtitle tracks, bounding the map.
func parseFrameCounts(tags []byte, counts map[uint64]int64) {
	matroska.Children(tags, func(id uint64, tag []byte) {
		if id != matroska.IDTag {
			return
		}
		frames := int64(-1)
		matroska.Children(tag, func(id uint64, v []byte) {
			if id != matroska.IDSimpleTag {
				return
			}
			var name, value string
			matroska.Children(v, func(id uint64, v []byte) {
				switch id {
				case matroska.IDTagName:
					name = matroska.String(v)
				case matroska.IDTagString:
					value = matroska.String(v)
				}
			})
			if name == "NUMBER_OF_FRAMES" {
				if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && n >= 0 {
					frames = n
				}
			}
		})
		if frames < 0 {
			return
		}
		// Targets may precede the statistic. Visit them after finding its
		// value without retaining a list that a crafted tag could expand.
		matroska.Children(tag, func(id uint64, v []byte) {
			if id != matroska.IDTargets {
				return
			}
			matroska.Children(v, func(id uint64, v []byte) {
				if id != matroska.IDTagTrackUID {
					return
				}
				uid := matroska.Uint(v)
				// A zero TagTrackUID targets every track, not one.
				if _, ok := counts[uid]; ok && uid != 0 {
					counts[uid] = frames
				}
			})
		})
	})
}

// collectCues fills each track's Cues from a Cues element, in file order
// and without duplicates. It reports false when the element is malformed.
//
// A crafted Cues element can hold over a million points, so it counts the
// subtitle ones first and allocates each list once.
func collectCues(cues []byte, tracks []Track, segStart, scale int64, b *budget) (bool, error) {
	byNumber := make(map[uint64]int, len(tracks))
	for i, t := range tracks {
		byNumber[t.Number] = i
	}
	counts := make([]int, len(tracks))
	total := 0
	if !matroska.WalkCuePoints(cues, func(_ uint64, hasTime bool, pos matroska.CueTrackPosition) {
		if i, ok := byNumber[pos.Track]; ok && hasTime && pos.ClusterPosition >= 0 {
			counts[i]++
			total++
		}
	}) || total > maxIndexCues {
		return false, nil
	}
	if b != nil {
		if err := b.take(total * int(unsafe.Sizeof(Cue{}))); err != nil {
			return false, err
		}
	}
	for i := range tracks {
		if counts[i] > 0 {
			tracks[i].Cues = make([]Cue, 0, counts[i])
		}
	}
	matroska.WalkCuePoints(cues, func(ticks uint64, hasTime bool, pos matroska.CueTrackPosition) {
		i, ok := byNumber[pos.Track]
		if !ok || !hasTime || pos.ClusterPosition < 0 || pos.ClusterPosition > math.MaxInt64-segStart || ticks > math.MaxInt64 {
			return
		}
		t, ok := ticksDuration(int64(ticks), scale)
		if !ok {
			return
		}
		c := Cue{Time: t, ClusterPosition: segStart + pos.ClusterPosition, RelativePosition: pos.RelativePosition}
		if pos.RelativePosition < 0 && pos.BlockNumber > 1 {
			// The cluster scan matches by timestamp, not CueBlockNumber.
			// Let FFmpeg preserve simultaneous blocks with distinct identities.
			tracks[i].Unsupported = "CueBlockNumber without CueRelativePosition"
		}
		if pos.Duration >= 0 {
			if d, ok := ticksDuration(pos.Duration, scale); ok {
				c.Duration = d
			}
		}
		tracks[i].Cues = append(tracks[i].Cues, c)
	})
	for i := range tracks {
		t := &tracks[i]
		slices.SortFunc(t.Cues, compareCues)
		t.Cues = slices.CompactFunc(t.Cues, sameBlock)
		t.allCueDurations = len(t.Cues) > 0
		for _, c := range t.Cues {
			t.maxCueDuration = max(t.maxCueDuration, c.Duration)
			if c.Duration <= 0 {
				t.allCueDurations = false
			}
		}
	}
	return true, nil
}

// compareCues orders cues by where their blocks are.
func compareCues(a, b Cue) int {
	if a.ClusterPosition != b.ClusterPosition {
		return cmpInt(a.ClusterPosition, b.ClusterPosition)
	}
	if a.RelativePosition != b.RelativePosition {
		return cmpInt(a.RelativePosition, b.RelativePosition)
	}
	return cmpInt(int64(a.Time), int64(b.Time))
}

// sameBlock reports whether two cues point at the same block. Without a
// relative position only the time tells blocks of one cluster apart.
func sameBlock(a, b Cue) bool {
	if a.ClusterPosition != b.ClusterPosition || a.RelativePosition != b.RelativePosition {
		return false
	}
	return a.RelativePosition >= 0 || a.Time == b.Time
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ticksDuration converts Matroska ticks to a duration, reporting false when
// it doesn't fit.
func ticksDuration(ticks, scale int64) (time.Duration, bool) {
	if scale <= 0 {
		return 0, false
	}
	if ticks > math.MaxInt64/scale || ticks < math.MinInt64/scale {
		return 0, false
	}
	return time.Duration(ticks * scale), true
}
