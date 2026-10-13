package mediaprobe

import (
	"errors"
	"fmt"
	"io"
)

// ErrNotMatroska reports that a file does not start with an EBML header
// declaring a Matroska or WebM document.
var ErrNotMatroska = errors.New("mediaprobe: not a Matroska file")

// ErrMatroskaTracksNotFound reports a Matroska file whose Tracks element could
// not be located before the first Cluster or through the seek head.
var ErrMatroskaTracksNotFound = errors.New("mediaprobe: Matroska Tracks element not found")

// Matroska track types, from the Matroska specification.
const (
	MatroskaTrackTypeVideo    = 1
	MatroskaTrackTypeAudio    = 2
	MatroskaTrackTypeSubtitle = 17
	MatroskaTrackTypeMetadata = 33
)

// MatroskaTrack is one TrackEntry of a Matroska Tracks element, in file order.
// Fields the entry omits are zero; Matroska has no default for TrackNumber or
// TrackType.
type MatroskaTrack struct {
	Number  uint64
	Type    uint64
	CodecID string
}

const (
	ebmlIDHeader       = 0x1A45DFA3
	ebmlIDDocType      = 0x4282
	mkvIDSegment       = 0x18538067
	mkvIDSeekHead      = 0x114D9B74
	mkvIDSeek          = 0x4DBB
	mkvIDSeekID        = 0x53AB
	mkvIDSeekPosition  = 0x53AC
	mkvIDTracks        = 0x1654AE6B
	mkvIDTrackEntry    = 0xAE
	mkvIDTrackNumber   = 0xD7
	mkvIDTrackType     = 0x83
	mkvIDCodecID       = 0x86
	mkvIDCluster       = 0x1F43B675
	ebmlUnknownSize    = -1
	maxEBMLHeaderSize  = 4 << 10
	maxSeekHeadSize    = 1 << 20
	maxTracksSize      = 16 << 20
	maxTopLevelVisited = 64
	// maxTrackEntries is FFmpeg's default max_streams. FFprobe refuses a file
	// with more TrackEntries, so none of them could match a probed stream, and
	// the cap keeps a corrupt Tracks payload from decoding millions of entries.
	maxTrackEntries = 1000
)

// ReadMatroskaTracks returns the TrackEntry list of a Matroska or WebM file.
// It reads the EBML header, the top-level elements ahead of the first
// Cluster, and the seek head's Tracks pointer when Tracks is stored later; it
// never reads media data. Muxers write Tracks near the start, so on a typical
// file this is a handful of small reads.
//
// When r fails a read, the error wraps r's error, so callers can tell storage
// that failed from a file that is not Matroska; ErrNotMatroska and
// ErrMatroskaTracksNotFound always describe the bytes read.
func ReadMatroskaTracks(r io.ReaderAt, size int64) ([]MatroskaTrack, error) {
	recorder := &readErrorRecorder{r: r}
	tracks, err := readMatroskaTracks(recorder, size)
	if err != nil && recorder.err != nil {
		// A failed read can look like malformed input to the parser.
		return nil, fmt.Errorf("mediaprobe: read Matroska file: %w", recorder.err)
	}
	return tracks, err
}

// readErrorRecorder keeps the first error its reader returns other than
// io.EOF, which only marks the end of a short file.
type readErrorRecorder struct {
	r   io.ReaderAt
	err error
}

func (rec *readErrorRecorder) ReadAt(p []byte, off int64) (int, error) {
	n, err := rec.r.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) && rec.err == nil {
		rec.err = err
	}
	return n, err
}

func readMatroskaTracks(r io.ReaderAt, size int64) ([]MatroskaTrack, error) {
	id, dataSize, headerLen, err := readEBMLElementHeader(r, 0)
	if err != nil || id != ebmlIDHeader {
		return nil, ErrNotMatroska
	}
	header, err := readEBMLElementData(r, headerLen, dataSize, maxEBMLHeaderSize)
	if err != nil {
		return nil, ErrNotMatroska
	}
	docType := ""
	ebmlChildren(header, func(id uint64, v []byte) {
		if id == ebmlIDDocType {
			docType = ebmlString(v)
		}
	})
	// An absent DocType takes the EBML default, "matroska".
	if docType != "" && docType != "matroska" && docType != "webm" {
		return nil, ErrNotMatroska
	}

	pos := headerLen + dataSize
	id, segSize, headerLen, err := readEBMLElementHeader(r, pos)
	if err != nil {
		return nil, fmt.Errorf("mediaprobe: read Matroska segment: %w", err)
	}
	if id != mkvIDSegment {
		return nil, fmt.Errorf("mediaprobe: Matroska segment missing at %d", pos)
	}
	segStart := pos + headerLen
	segEnd := size
	if segSize != ebmlUnknownSize && segStart+segSize < segEnd {
		segEnd = segStart + segSize
	}

	tracksAt := int64(-1)
	for at, visited := segStart, 0; at < segEnd && visited < maxTopLevelVisited; visited++ {
		id, dataSize, headerLen, err := readEBMLElementHeader(r, at)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, fmt.Errorf("mediaprobe: read Matroska element at %d: %w", at, err)
		}
		switch id {
		case mkvIDTracks:
			return readMatroskaTracksElement(r, at+headerLen, dataSize)
		case mkvIDSeekHead:
			// A damaged seek head only loses the fallback; keep scanning.
			if data, err := readEBMLElementData(r, at+headerLen, dataSize, maxSeekHeadSize); err == nil && tracksAt < 0 {
				if p, ok := matroskaSeekPosition(data, mkvIDTracks); ok {
					tracksAt = segStart + p
				}
			}
		}
		// Clusters hold the media. Stop the linear scan at the first one and
		// follow the seek head instead.
		if id == mkvIDCluster || dataSize == ebmlUnknownSize {
			break
		}
		at += headerLen + dataSize
	}

	if tracksAt < segStart || tracksAt >= segEnd {
		return nil, ErrMatroskaTracksNotFound
	}
	id, dataSize, headerLen, err = readEBMLElementHeader(r, tracksAt)
	if err != nil {
		return nil, fmt.Errorf("mediaprobe: read Matroska Tracks at %d: %w", tracksAt, err)
	}
	if id != mkvIDTracks {
		return nil, ErrMatroskaTracksNotFound
	}
	return readMatroskaTracksElement(r, tracksAt+headerLen, dataSize)
}

func readMatroskaTracksElement(r io.ReaderAt, off, size int64) ([]MatroskaTrack, error) {
	data, err := readEBMLElementData(r, off, size, maxTracksSize)
	if err != nil {
		return nil, err
	}
	var tracks []MatroskaTrack
	ebmlChildren(data, func(id uint64, entry []byte) {
		if id != mkvIDTrackEntry || len(tracks) > maxTrackEntries {
			return
		}
		var track MatroskaTrack
		ebmlChildren(entry, func(id uint64, v []byte) {
			switch id {
			case mkvIDTrackNumber:
				track.Number = ebmlUint(v)
			case mkvIDTrackType:
				track.Type = ebmlUint(v)
			case mkvIDCodecID:
				track.CodecID = ebmlString(v)
			}
		})
		tracks = append(tracks, track)
	})
	if len(tracks) > maxTrackEntries {
		return nil, fmt.Errorf("mediaprobe: Matroska Tracks has more than %d entries", maxTrackEntries)
	}
	return tracks, nil
}

// readEBMLElementHeader reads the ID and data size of the element at off and
// returns the header's length. A size of ebmlUnknownSize means the element
// runs to the end of its parent.
func readEBMLElementHeader(r io.ReaderAt, off int64) (id uint64, size int64, headerLen int64, err error) {
	var buf [16]byte
	n, err := r.ReadAt(buf[:], off)
	if n == 0 {
		if err == nil {
			err = io.EOF
		}
		return 0, 0, 0, err
	}
	b := buf[:n]
	id, idLen, ok := readEBMLVint(b, true)
	if !ok || idLen > 4 {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	raw, sizeLen, ok := readEBMLVint(b[idLen:], false)
	if !ok {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	if raw == (uint64(1)<<(7*sizeLen))-1 {
		return id, ebmlUnknownSize, int64(idLen + sizeLen), nil
	}
	if raw > 1<<62 {
		return 0, 0, 0, fmt.Errorf("element size %d out of range", raw)
	}
	return id, int64(raw), int64(idLen + sizeLen), nil
}

func readEBMLElementData(r io.ReaderAt, off, size, limit int64) ([]byte, error) {
	if size < 0 || size > limit {
		return nil, fmt.Errorf("mediaprobe: Matroska element at %d has unsupported size %d", off, size)
	}
	data := make([]byte, size)
	if _, err := r.ReadAt(data, off); err != nil {
		return nil, fmt.Errorf("mediaprobe: read Matroska element at %d: %w", off, err)
	}
	return data, nil
}

// readEBMLVint reads an EBML variable-length integer. IDs keep their length
// marker bit; sizes drop it.
func readEBMLVint(b []byte, keepMarker bool) (value uint64, length int, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	length = 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		length++
	}
	if len(b) < length {
		return 0, 0, false
	}
	value = uint64(b[0])
	if !keepMarker {
		value &= uint64(0xFF >> length)
	}
	for _, c := range b[1:length] {
		value = value<<8 | uint64(c)
	}
	return value, length, true
}

// ebmlChildren calls fn for each child element of a master element's data.
// It stops at the first malformed or unknown-size child.
func ebmlChildren(data []byte, fn func(id uint64, payload []byte)) {
	for len(data) > 0 {
		id, idLen, ok := readEBMLVint(data, true)
		if !ok {
			return
		}
		raw, sizeLen, ok := readEBMLVint(data[idLen:], false)
		if !ok {
			return
		}
		start := idLen + sizeLen
		if raw > uint64(len(data)-start) {
			return
		}
		end := start + int(raw)
		fn(id, data[start:end])
		data = data[end:]
	}
}

func ebmlUint(b []byte) uint64 {
	if len(b) > 8 {
		return 0
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// ebmlString decodes an EBML string, which may be padded with trailing zero
// bytes.
func ebmlString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func matroskaSeekPosition(seekHead []byte, target uint64) (int64, bool) {
	var (
		pos   int64
		found bool
	)
	ebmlChildren(seekHead, func(id uint64, seek []byte) {
		if id != mkvIDSeek || found {
			return
		}
		var seekID uint64
		seekPos := int64(-1)
		ebmlChildren(seek, func(id uint64, v []byte) {
			switch id {
			case mkvIDSeekID:
				seekID = ebmlUint(v)
			case mkvIDSeekPosition:
				if p := ebmlUint(v); p < 1<<62 {
					seekPos = int64(p)
				}
			}
		})
		if seekID == target && seekPos >= 0 {
			pos, found = seekPos, true
		}
	})
	return pos, found
}
