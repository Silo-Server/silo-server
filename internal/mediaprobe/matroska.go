package mediaprobe

import (
	"errors"
	"fmt"
	"io"

	"github.com/Silo-Server/silo-server/internal/matroska"
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
	// LanguageBCP47 is the entry's BCP 47 language tag, such as "pt-BR".
	// Matroska makes it override the legacy ISO 639-2 Language element,
	// which is all FFprobe reports, so a region or script is lost without it.
	LanguageBCP47 string
}

const (
	ebmlIDHeader       = matroska.IDEBML
	ebmlIDDocType      = matroska.IDDocType
	mkvIDSegment       = matroska.IDSegment
	mkvIDSeekHead      = matroska.IDSeekHead
	mkvIDSeek          = matroska.IDSeek
	mkvIDSeekID        = matroska.IDSeekID
	mkvIDSeekPosition  = matroska.IDSeekPosition
	mkvIDTracks        = matroska.IDTracks
	mkvIDTrackEntry    = matroska.IDTrackEntry
	mkvIDTrackNumber   = matroska.IDTrackNumber
	mkvIDTrackType     = matroska.IDTrackType
	mkvIDCodecID       = matroska.IDCodecID
	mkvIDCluster       = matroska.IDCluster
	ebmlUnknownSize    = matroska.UnknownSize
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
	id, dataSize, headerLen, err := matroska.ReadElementHeader(r, 0)
	if err != nil || id != ebmlIDHeader {
		return nil, ErrNotMatroska
	}
	header, err := matroska.ReadElementData(r, headerLen, dataSize, maxEBMLHeaderSize)
	if err != nil {
		return nil, ErrNotMatroska
	}
	docType := ""
	matroska.Children(header, func(id uint64, v []byte) {
		if id == ebmlIDDocType {
			docType = matroska.String(v)
		}
	})
	// An absent DocType takes the EBML default, "matroska".
	if docType != "" && docType != "matroska" && docType != "webm" {
		return nil, ErrNotMatroska
	}

	pos := headerLen + dataSize
	id, segSize, headerLen, err := matroska.ReadElementHeader(r, pos)
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
		id, dataSize, headerLen, err := matroska.ReadElementHeader(r, at)
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
			if data, err := matroska.ReadElementData(r, at+headerLen, dataSize, maxSeekHeadSize); err == nil && tracksAt < 0 {
				if p, ok := matroska.SeekPosition(data, mkvIDTracks); ok {
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
	id, dataSize, headerLen, err = matroska.ReadElementHeader(r, tracksAt)
	if err != nil {
		return nil, fmt.Errorf("mediaprobe: read Matroska Tracks at %d: %w", tracksAt, err)
	}
	if id != mkvIDTracks {
		return nil, ErrMatroskaTracksNotFound
	}
	return readMatroskaTracksElement(r, tracksAt+headerLen, dataSize)
}

func readMatroskaTracksElement(r io.ReaderAt, off, size int64) ([]MatroskaTrack, error) {
	data, err := matroska.ReadElementData(r, off, size, maxTracksSize)
	if err != nil {
		return nil, err
	}
	var tracks []MatroskaTrack
	matroska.Children(data, func(id uint64, entry []byte) {
		if id != mkvIDTrackEntry || len(tracks) > maxTrackEntries {
			return
		}
		e := matroska.ParseTrackEntry(entry)
		tracks = append(tracks, MatroskaTrack{Number: e.Number, Type: e.Type, CodecID: e.CodecID, LanguageBCP47: e.LanguageBCP47})
	})
	if len(tracks) > maxTrackEntries {
		return nil, fmt.Errorf("mediaprobe: Matroska Tracks has more than %d entries", maxTrackEntries)
	}
	return tracks, nil
}
