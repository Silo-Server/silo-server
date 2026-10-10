// Package keyframes reads a media file's video keyframe times from its
// container index, without reading the media itself, and plans HLS segment
// boundaries on them.
package keyframes

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

// ErrNoIndex means the file has no usable keyframe index: no Cues, or none
// for its video track.
var ErrNoIndex = errors.New("keyframes: no keyframe index")

// notMatroskaError reports a file that isn't Matroska or WebM: a stable fact
// about the file, unlike a read failure.
type notMatroskaError struct{}

func (notMatroskaError) Error() string { return "keyframes: not a Matroska file" }

func isShortRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// Index is a file's video keyframe times, in seconds on the container's
// timeline, ascending and without duplicates.
type Index struct {
	Keyframes []float64
	// Duration is the container's duration in seconds, or 0 when unknown.
	Duration float64
	// VideoEnd is when the video track's last frame ends, in seconds on the
	// same timeline as Keyframes, or 0 when unknown. Only VerifyMatroska,
	// which reads every block, sets it. Other tracks can run longer, so it
	// can be earlier than Duration.
	VideoEnd float64
}

// idTrackTimestampScale is a track's deprecated extra timestamp scale.
const idTrackTimestampScale = 0x23314F

// Matroska element IDs, from the Matroska specification.
const (
	idEBML                  = 0x1A45DFA3
	idSegment               = 0x18538067
	idSeekHead              = 0x114D9B74
	idSeek                  = 0x4DBB
	idSeekID                = 0x53AB
	idSeekPosition          = 0x53AC
	idInfo                  = 0x1549A966
	idTimestampScale        = 0x2AD7B1
	idDuration              = 0x4489
	idTracks                = 0x1654AE6B
	idTrackEntry            = 0xAE
	idTrackNumber           = 0xD7
	idTrackType             = 0x83
	idDefaultDuration       = 0x23E383
	idCues                  = 0x1C53BB6B
	idCuePoint              = 0xBB
	idCueTime               = 0xB3
	idCueTrackPos           = 0xB7
	idCueTrack              = 0xF7
	idCluster               = 0x1F43B675
	trackTypeVideo          = 1
	defaultTimescale        = 1_000_000 // nanoseconds per timestamp tick
	unknownSize       int64 = -1
)

// Upper bounds on what the reader loads into memory. Cues for a long film
// with every keyframe indexed are a few megabytes; the metadata elements are
// small. Anything larger is treated as a damaged file.
const (
	maxCuesSize     = 64 << 20
	maxMetadataSize = 16 << 20
)

// ReadMatroska returns the keyframe index of a Matroska or WebM file from its
// Cues element. It reads the header, the seek head, segment info, tracks and
// cues, following the seek head to Cues stored after the clusters. As a quick
// check it reads the block headers of a few clusters (checkCueCoverage);
// VerifyMatroska checks them all.
func ReadMatroska(r io.ReaderAt, size int64) (Index, error) {
	m, err := readMatroska(r, size)
	return m.Index, err
}

// matroskaIndex is an Index with what checking it against the clusters
// needs (VerifyMatroska).
type matroskaIndex struct {
	Index
	segStart, segEnd int64
	video            uint64
	points           []cuePoint
	timescale        int64
	// frameSeconds is the video track's default frame duration, or 0.
	frameSeconds float64
}

func readMatroska(r io.ReaderAt, size int64) (matroskaIndex, error) {
	id, dataSize, headerLen, err := readElementHeader(r, 0)
	if err != nil {
		if isShortRead(err) {
			// Empty, or not starting with an EBML element at all.
			return matroskaIndex{}, notMatroskaError{}
		}
		return matroskaIndex{}, fmt.Errorf("keyframes: read EBML header: %w", err)
	}
	if id != idEBML {
		return matroskaIndex{}, notMatroskaError{}
	}
	pos := headerLen + dataSize

	id, segSize, headerLen, err := readElementHeader(r, pos)
	if err != nil {
		if isShortRead(err) {
			return matroskaIndex{}, notMatroskaError{}
		}
		return matroskaIndex{}, fmt.Errorf("keyframes: read segment: %w", err)
	}
	if id != idSegment {
		return matroskaIndex{}, notMatroskaError{}
	}
	segStart := pos + headerLen
	segEnd := size
	if segSize != unknownSize && segStart+segSize < segEnd {
		segEnd = segStart + segSize
	}

	var (
		timescale int64 = defaultTimescale
		duration  float64
		video     uint64
		frameNS   uint64
		cuesAt    int64 = -1
		infoAt    int64 = -1
		tracksAt  int64 = -1
		cues      []byte
		haveInfo  bool
		haveTrack bool
	)
	for at := segStart; at < segEnd; {
		id, dataSize, headerLen, err := readElementHeader(r, at)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return matroskaIndex{}, fmt.Errorf("keyframes: read element at %d: %w", at, err)
		}
		dataAt := at + headerLen
		switch id {
		case idSeekHead:
			data, err := readElementData(r, dataAt, dataSize, maxMetadataSize)
			if err != nil {
				return matroskaIndex{}, err
			}
			if p, ok := seekPosition(data, idCues); ok {
				cuesAt = segStart + p
			}
			if p, ok := seekPosition(data, idInfo); ok {
				infoAt = segStart + p
			}
			if p, ok := seekPosition(data, idTracks); ok {
				tracksAt = segStart + p
			}
		case idInfo:
			data, err := readElementData(r, dataAt, dataSize, maxMetadataSize)
			if err != nil {
				return matroskaIndex{}, err
			}
			var ok bool
			if timescale, duration, ok = parseInfo(data); !ok {
				return matroskaIndex{}, ErrNoIndex
			}
			haveInfo = true
		case idTracks:
			data, err := readElementData(r, dataAt, dataSize, maxMetadataSize)
			if err != nil {
				return matroskaIndex{}, err
			}
			var scaled bool
			video, frameNS, scaled, haveTrack = firstVideoTrack(data)
			if scaled {
				// The track's timestamps are scaled again, a deprecated
				// feature the times here don't apply; don't plan from them.
				return matroskaIndex{}, ErrNoIndex
			}
		case idCues:
			cues, err = readElementData(r, dataAt, dataSize, maxCuesSize)
			if err != nil {
				return matroskaIndex{}, err
			}
		}
		if cues != nil && haveInfo && haveTrack {
			break
		}
		// Clusters hold the media. Stop the linear scan at the first one and
		// jump to the Cues the seek head points at, if they come later.
		if id == idCluster || dataSize == unknownSize {
			break
		}
		at = dataAt + dataSize
	}

	if cues == nil && cuesAt >= 0 {
		id, dataSize, headerLen, err := readElementHeader(r, cuesAt)
		if err != nil {
			return matroskaIndex{}, fmt.Errorf("keyframes: read cues at %d: %w", cuesAt, err)
		}
		if id != idCues {
			return matroskaIndex{}, fmt.Errorf("keyframes: seek head points at element %#x, not Cues", id)
		}
		cues, err = readElementData(r, cuesAt+headerLen, dataSize, maxCuesSize)
		if err != nil {
			return matroskaIndex{}, err
		}
	}
	// Info and Tracks may also come after the clusters, found through the
	// seek head. Info's timestamp scale converts every cue time, so an index
	// without it isn't trusted.
	if !haveInfo && infoAt >= 0 {
		data, err := readSeekTarget(r, infoAt, idInfo)
		if err != nil {
			return matroskaIndex{}, err
		}
		var ok bool
		if timescale, duration, ok = parseInfo(data); !ok {
			return matroskaIndex{}, ErrNoIndex
		}
		haveInfo = true
	}
	if !haveTrack && tracksAt >= 0 {
		data, err := readSeekTarget(r, tracksAt, idTracks)
		if err != nil {
			return matroskaIndex{}, err
		}
		var scaled bool
		video, frameNS, scaled, haveTrack = firstVideoTrack(data)
		if scaled {
			return matroskaIndex{}, ErrNoIndex
		}
	}
	if cues == nil || !haveTrack || !haveInfo {
		return matroskaIndex{}, ErrNoIndex
	}

	points, complete := cuePoints(cues, video)
	if !complete || len(points) == 0 {
		// A malformed cue point ends the list early; the points read so
		// far would leave out later keyframes.
		return matroskaIndex{}, ErrNoIndex
	}
	if err := checkCueCoverage(r, segStart, segEnd, video, points); err != nil {
		return matroskaIndex{}, err
	}
	ticks := make([]uint64, len(points))
	for i, p := range points {
		ticks[i] = p.ticks
	}
	sort.Slice(ticks, func(i, j int) bool { return ticks[i] < ticks[j] })
	idx := Index{Keyframes: make([]float64, 0, len(ticks))}
	for i, t := range ticks {
		if i > 0 && t == ticks[i-1] {
			continue
		}
		idx.Keyframes = append(idx.Keyframes, float64(t)*float64(timescale)/1e9)
	}
	if duration > 0 {
		idx.Duration = duration * float64(timescale) / 1e9
	}
	return matroskaIndex{
		Index: idx, segStart: segStart, segEnd: segEnd, video: video, points: points,
		timescale: timescale, frameSeconds: float64(frameNS) / 1e9,
	}, nil
}

// readElementHeader reads an element's ID and data size at off, returning the
// header's length. A data size of unknownSize means the element runs to the
// end of its parent.
func readElementHeader(r io.ReaderAt, off int64) (id uint64, size int64, headerLen int64, err error) {
	var buf [12]byte
	n, err := r.ReadAt(buf[:], off)
	if n == 0 {
		if err == nil {
			err = io.EOF
		}
		return 0, 0, 0, err
	}
	b := buf[:n]
	id, idLen, ok := readVint(b, true)
	if !ok {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	raw, sizeLen, ok := readVint(b[idLen:], false)
	if !ok {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	size = int64(raw)
	if raw == (uint64(1)<<(7*sizeLen))-1 {
		size = unknownSize
	}
	return id, size, int64(idLen + sizeLen), nil
}

func readElementData(r io.ReaderAt, off, size, limit int64) ([]byte, error) {
	if size == unknownSize || size < 0 || size > limit {
		return nil, fmt.Errorf("keyframes: element at %d has unsupported size %d", off, size)
	}
	data := make([]byte, size)
	if _, err := r.ReadAt(data, off); err != nil {
		return nil, fmt.Errorf("keyframes: read element at %d: %w", off, err)
	}
	return data, nil
}

// readVint reads an EBML variable-length integer. IDs keep their length
// marker bit; sizes drop it.
func readVint(b []byte, keepMarker bool) (value uint64, length int, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	length = 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		length++
	}
	if length > 8 || len(b) < length {
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

// readSeekTarget reads the element a seek head points at, which must be want.
func readSeekTarget(r io.ReaderAt, at int64, want uint64) ([]byte, error) {
	id, dataSize, headerLen, err := readElementHeader(r, at)
	if err != nil {
		return nil, fmt.Errorf("keyframes: read element at %d: %w", at, err)
	}
	if id != want {
		return nil, fmt.Errorf("keyframes: seek head points at element %#x, not %#x", id, want)
	}
	return readElementData(r, at+headerLen, dataSize, maxMetadataSize)
}

// childrenComplete is children, reporting whether every child was well
// formed: false when malformed data stopped the walk early.
func childrenComplete(data []byte, fn func(id uint64, payload []byte)) bool {
	for len(data) > 0 {
		id, idLen, ok := readVint(data, true)
		if !ok {
			return false
		}
		raw, sizeLen, ok := readVint(data[idLen:], false)
		if !ok {
			return false
		}
		start := idLen + sizeLen
		if raw > uint64(len(data)-start) {
			return false
		}
		end := start + int(raw)
		fn(id, data[start:end])
		data = data[end:]
	}
	return true
}

// children calls fn for each child element in a master element's data. It
// stops at the first malformed child.
func children(data []byte, fn func(id uint64, payload []byte)) {
	for len(data) > 0 {
		id, idLen, ok := readVint(data, true)
		if !ok {
			return
		}
		raw, sizeLen, ok := readVint(data[idLen:], false)
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

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func readFloat(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}

func seekPosition(seekHead []byte, target uint64) (int64, bool) {
	var (
		pos   int64
		found bool
	)
	children(seekHead, func(id uint64, seek []byte) {
		if id != idSeek || found {
			return
		}
		var seekID uint64
		var seekPos int64 = -1
		children(seek, func(id uint64, v []byte) {
			switch id {
			case idSeekID:
				seekID = readUint(v)
			case idSeekPosition:
				seekPos = int64(readUint(v))
			}
		})
		if seekID == target && seekPos >= 0 {
			pos, found = seekPos, true
		}
	})
	return pos, found
}

// parseInfo reads Info's timestamp scale, defaulting when it is absent, and
// duration. ok is false for an explicit scale Matroska doesn't allow (zero)
// or that doesn't fit: cue times scaled by a guess would plan wrong segments.
func parseInfo(info []byte) (timescale int64, duration float64, ok bool) {
	timescale, ok = defaultTimescale, true
	children(info, func(id uint64, v []byte) {
		switch id {
		case idTimestampScale:
			if s := int64(readUint(v)); s > 0 {
				timescale = s
			} else {
				ok = false
			}
		case idDuration:
			duration = readFloat(v)
		}
	})
	return timescale, duration, ok
}

func firstVideoTrack(tracks []byte) (number, frameNS uint64, scaled, found bool) {
	children(tracks, func(id uint64, entry []byte) {
		if id != idTrackEntry || found {
			return
		}
		var n, kind, frame uint64
		trackScale := 1.0
		children(entry, func(id uint64, v []byte) {
			switch id {
			case idTrackNumber:
				n = readUint(v)
			case idTrackType:
				kind = readUint(v)
			case idDefaultDuration:
				frame = readUint(v)
			case idTrackTimestampScale:
				trackScale = readFloat(v)
			}
		})
		if kind == trackTypeVideo && n > 0 {
			number, frameNS, scaled, found = n, frame, trackScale != 1, true
		}
	})
	return number, frameNS, scaled, found
}

// cuePoints returns every cue point that indexes the video track, with the
// position of the cluster holding it (-1 when the cue doesn't say). Matroska
// muxers index video keyframes, so the times are keyframe times; whether
// every keyframe has one is checked separately (checkCueCoverage).
// It reports false when a malformed cue point stopped the walk early.
func cuePoints(cues []byte, video uint64) ([]cuePoint, bool) {
	var points []cuePoint
	complete := true
	complete = childrenComplete(cues, func(id uint64, point []byte) {
		if id != idCuePoint {
			return
		}
		var (
			t       uint64
			hasTime bool
			isVideo bool
			cluster int64 = -1
		)
		if !childrenComplete(point, func(id uint64, v []byte) {
			switch id {
			case idCueTime:
				t, hasTime = readUint(v), true
			case idCueTrackPos:
				var (
					track uint64
					pos   int64 = -1
				)
				// Malformed data here could hide the video track's entry.
				if !childrenComplete(v, func(id uint64, v []byte) {
					switch id {
					case idCueTrack:
						track = readUint(v)
					case idCueClusterPosition:
						pos = int64(readUint(v))
					}
				}) {
					complete = false
				}
				if track == video {
					isVideo, cluster = true, pos
				}
			}
		}) {
			complete = false
		}
		if hasTime && isVideo {
			points = append(points, cuePoint{ticks: t, cluster: cluster})
		}
	}) && complete
	return points, complete
}
