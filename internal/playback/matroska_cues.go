package playback

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

const (
	ebmlIDSegment           = 0x18538067
	ebmlIDSeekHead          = 0x114d9b74
	ebmlIDSeek              = 0x4dbb
	ebmlIDSeekID            = 0x53ab
	ebmlIDSeekPosition      = 0x53ac
	ebmlIDInfo              = 0x1549a966
	ebmlIDTimestampScale    = 0x2ad7b1
	ebmlIDDuration          = 0x4489
	ebmlIDTracks            = 0x1654ae6b
	ebmlIDTrackEntry        = 0xae
	ebmlIDTrackNumber       = 0xd7
	ebmlIDTrackType         = 0x83
	ebmlTrackTypeVideo      = 1
	ebmlIDCues              = 0x1c53bb6b
	ebmlIDCuePoint          = 0xbb
	ebmlIDCueTime           = 0xb3
	ebmlIDCueTrackPositions = 0xb7
	ebmlIDCueTrack          = 0xf7
	ebmlIDCluster           = 0x1f43b675
	defaultTimestampScale   = 1_000_000
)

type ebmlElement struct {
	id      uint64
	data    int64
	end     int64
	unknown bool
}

type ebmlFileReader struct {
	file *os.File
	ctx  context.Context
}

// probeMatroskaCueTimeline reads only EBML element headers plus Info, Tracks,
// and Cues payloads. Clusters are skipped with seeks, so extracting a multi-GB
// movie timeline does not compete with active playback for a sequential scan.
func probeMatroskaCueTimeline(ctx context.Context, path string) ([]float64, float64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open Matroska source: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat Matroska source: %w", err)
	}
	r := &ebmlFileReader{file: file, ctx: ctx}
	segment, err := r.findElement(0, info.Size(), ebmlIDSegment)
	if err != nil {
		return nil, 0, fmt.Errorf("find Matroska Segment: %w", err)
	}
	segmentEnd := segment.end
	if segment.unknown {
		segmentEnd = info.Size()
	}

	infoElement, tracksElement, cuesElement, err := r.findMatroskaMetadata(segment.data, segmentEnd)
	if err != nil {
		return nil, 0, err
	}

	timestampScale, durationUnits, err := r.readMatroskaInfo(infoElement)
	if err != nil {
		return nil, 0, err
	}
	videoTrack, err := r.readFirstVideoTrack(tracksElement)
	if err != nil {
		return nil, 0, err
	}
	keyframeUnits, err := r.readVideoCueTimes(cuesElement, videoTrack)
	if err != nil {
		return nil, 0, err
	}
	scaleSeconds := float64(timestampScale) / 1_000_000_000
	keyframes := make([]float64, len(keyframeUnits))
	for i, cue := range keyframeUnits {
		keyframes[i] = float64(cue) * scaleSeconds
	}
	return keyframes, durationUnits * scaleSeconds, nil
}

// findMatroskaMetadata follows SeekHead offsets instead of walking every
// Cluster. A long movie can contain tens of thousands of Clusters, and even
// header-only seeks across all of them are prohibitively slow on network
// storage. Seek positions are relative to the Segment payload.
func (r *ebmlFileReader) findMatroskaMetadata(segmentStart, segmentEnd int64) (ebmlElement, ebmlElement, ebmlElement, error) {
	var infoElement, tracksElement, cuesElement ebmlElement
	seekHeads := make([]ebmlElement, 0, 2)
	err := r.forEachChild(segmentStart, segmentEnd, func(element ebmlElement) error {
		switch element.id {
		case ebmlIDSeekHead:
			seekHeads = append(seekHeads, element)
		case ebmlIDInfo:
			infoElement = element
		case ebmlIDTracks:
			tracksElement = element
		case ebmlIDCues:
			cuesElement = element
		case ebmlIDCluster:
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return ebmlElement{}, ebmlElement{}, ebmlElement{}, fmt.Errorf("scan Matroska Segment metadata: %w", err)
	}

	visited := make(map[int64]bool)
	for len(seekHeads) > 0 {
		head := seekHeads[0]
		seekHeads = seekHeads[1:]
		if visited[head.data] {
			continue
		}
		visited[head.data] = true
		entries, err := r.readSeekHead(head)
		if err != nil {
			return ebmlElement{}, ebmlElement{}, ebmlElement{}, fmt.Errorf("read Matroska SeekHead: %w", err)
		}
		for id, relativePosition := range entries {
			if relativePosition > uint64(math.MaxInt64-segmentStart) {
				return ebmlElement{}, ebmlElement{}, ebmlElement{}, fmt.Errorf("matroska seek position overflows file offset")
			}
			position := segmentStart + int64(relativePosition)
			element, err := r.readElementAt(position, segmentEnd)
			if err != nil {
				return ebmlElement{}, ebmlElement{}, ebmlElement{}, fmt.Errorf("read indexed Matroska element %#x: %w", id, err)
			}
			if element.id != id {
				return ebmlElement{}, ebmlElement{}, ebmlElement{}, fmt.Errorf("matroska SeekHead expected element %#x at %d, found %#x", id, position, element.id)
			}
			switch id {
			case ebmlIDSeekHead:
				seekHeads = append(seekHeads, element)
			case ebmlIDInfo:
				infoElement = element
			case ebmlIDTracks:
				tracksElement = element
			case ebmlIDCues:
				cuesElement = element
			}
		}
		if infoElement.id != 0 && tracksElement.id != 0 && cuesElement.id != 0 {
			break
		}
	}
	if infoElement.id == 0 || tracksElement.id == 0 || cuesElement.id == 0 {
		return ebmlElement{}, ebmlElement{}, ebmlElement{}, fmt.Errorf("matroska source is missing indexed Info, Tracks, or Cues")
	}
	return infoElement, tracksElement, cuesElement, nil
}

func (r *ebmlFileReader) readSeekHead(container ebmlElement) (map[uint64]uint64, error) {
	entries := make(map[uint64]uint64)
	err := r.forEachChild(container.data, container.end, func(entry ebmlElement) error {
		if entry.id != ebmlIDSeek {
			return nil
		}
		var id, position uint64
		if err := r.forEachChild(entry.data, entry.end, func(element ebmlElement) error {
			var err error
			switch element.id {
			case ebmlIDSeekID:
				id, err = r.readBinaryID(element)
			case ebmlIDSeekPosition:
				position, err = r.readUint(element)
			}
			return err
		}); err != nil {
			return err
		}
		if id != 0 {
			entries[id] = position
		}
		return nil
	})
	return entries, err
}

func (r *ebmlFileReader) readMatroskaInfo(container ebmlElement) (uint64, float64, error) {
	timestampScale := uint64(defaultTimestampScale)
	var duration float64
	err := r.forEachChild(container.data, container.end, func(element ebmlElement) error {
		var err error
		switch element.id {
		case ebmlIDTimestampScale:
			timestampScale, err = r.readUint(element)
		case ebmlIDDuration:
			duration, err = r.readFloat(element)
		}
		return err
	})
	if err != nil {
		return 0, 0, fmt.Errorf("read Matroska Info: %w", err)
	}
	if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return 0, 0, fmt.Errorf("matroska Info has invalid duration %v", duration)
	}
	return timestampScale, duration, nil
}

func (r *ebmlFileReader) readFirstVideoTrack(container ebmlElement) (uint64, error) {
	var videoTrack uint64
	err := r.forEachChild(container.data, container.end, func(entry ebmlElement) error {
		if entry.id != ebmlIDTrackEntry || videoTrack != 0 {
			return nil
		}
		var number, trackType uint64
		if err := r.forEachChild(entry.data, entry.end, func(element ebmlElement) error {
			var err error
			switch element.id {
			case ebmlIDTrackNumber:
				number, err = r.readUint(element)
			case ebmlIDTrackType:
				trackType, err = r.readUint(element)
			}
			return err
		}); err != nil {
			return err
		}
		if trackType == ebmlTrackTypeVideo {
			videoTrack = number
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("read Matroska Tracks: %w", err)
	}
	if videoTrack == 0 {
		return 0, fmt.Errorf("matroska source has no video track")
	}
	return videoTrack, nil
}

func (r *ebmlFileReader) readVideoCueTimes(container ebmlElement, videoTrack uint64) ([]uint64, error) {
	keyframes := make([]uint64, 0, 2048)
	err := r.forEachChild(container.data, container.end, func(point ebmlElement) error {
		if point.id != ebmlIDCuePoint {
			return nil
		}
		var cueTime uint64
		matchesVideo := false
		if err := r.forEachChild(point.data, point.end, func(element ebmlElement) error {
			switch element.id {
			case ebmlIDCueTime:
				value, err := r.readUint(element)
				cueTime = value
				return err
			case ebmlIDCueTrackPositions:
				return r.forEachChild(element.data, element.end, func(position ebmlElement) error {
					if position.id != ebmlIDCueTrack {
						return nil
					}
					track, err := r.readUint(position)
					if track == videoTrack {
						matchesVideo = true
					}
					return err
				})
			}
			return nil
		}); err != nil {
			return err
		}
		if matchesVideo && (len(keyframes) == 0 || cueTime > keyframes[len(keyframes)-1]) {
			keyframes = append(keyframes, cueTime)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read Matroska Cues: %w", err)
	}
	if len(keyframes) == 0 {
		return nil, fmt.Errorf("matroska Cues contain no video keyframes")
	}
	return keyframes, nil
}

func (r *ebmlFileReader) findElement(start, end int64, id uint64) (ebmlElement, error) {
	var found ebmlElement
	err := r.forEachChild(start, end, func(element ebmlElement) error {
		if element.id == id {
			found = element
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return ebmlElement{}, err
	}
	if found.id == 0 {
		return ebmlElement{}, fmt.Errorf("element %#x not found", id)
	}
	return found, nil
}

func (r *ebmlFileReader) forEachChild(start, end int64, visit func(ebmlElement) error) error {
	if _, err := r.file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		position, err := r.file.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if position >= end {
			return nil
		}
		element, err := r.readElement(end)
		if err != nil {
			return err
		}
		if err := visit(element); err != nil {
			return err
		}
		if element.unknown {
			return fmt.Errorf("cannot skip unknown-sized element %#x", element.id)
		}
		if _, err := r.file.Seek(element.end, io.SeekStart); err != nil {
			return err
		}
	}
}

func (r *ebmlFileReader) readElement(containerEnd int64) (ebmlElement, error) {
	id, _, _, err := readEBMLVInt(r.file, true)
	if err != nil {
		return ebmlElement{}, err
	}
	size, _, unknown, err := readEBMLVInt(r.file, false)
	if err != nil {
		return ebmlElement{}, err
	}
	data, err := r.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return ebmlElement{}, err
	}
	end := containerEnd
	if !unknown {
		if size > uint64(math.MaxInt64-data) {
			return ebmlElement{}, fmt.Errorf("element %#x size overflows file offset", id)
		}
		end = data + int64(size)
		if end > containerEnd {
			return ebmlElement{}, fmt.Errorf("element %#x exceeds its container", id)
		}
	}
	return ebmlElement{id: id, data: data, end: end, unknown: unknown}, nil
}

func (r *ebmlFileReader) readElementAt(position, containerEnd int64) (ebmlElement, error) {
	if position < 0 || position >= containerEnd {
		return ebmlElement{}, fmt.Errorf("element offset %d is outside its container", position)
	}
	if _, err := r.file.Seek(position, io.SeekStart); err != nil {
		return ebmlElement{}, err
	}
	return r.readElement(containerEnd)
}

func readEBMLVInt(reader io.Reader, preserveMarker bool) (uint64, int, bool, error) {
	var first [1]byte
	if _, err := io.ReadFull(reader, first[:]); err != nil {
		return 0, 0, false, err
	}
	mask := byte(0x80)
	length := 1
	for length <= 8 && first[0]&mask == 0 {
		mask >>= 1
		length++
	}
	if length > 8 {
		return 0, 0, false, fmt.Errorf("invalid EBML variable integer")
	}
	value := uint64(first[0])
	if !preserveMarker {
		value = uint64(first[0] &^ mask)
	}
	for range length - 1 {
		var next [1]byte
		if _, err := io.ReadFull(reader, next[:]); err != nil {
			return 0, 0, false, err
		}
		value = value<<8 | uint64(next[0])
	}
	unknown := !preserveMarker && value == (uint64(1)<<(7*length))-1
	return value, length, unknown, nil
}

func (r *ebmlFileReader) readUint(element ebmlElement) (uint64, error) {
	size := element.end - element.data
	if size < 1 || size > 8 {
		return 0, fmt.Errorf("invalid unsigned integer size %d", size)
	}
	if _, err := r.file.Seek(element.data, io.SeekStart); err != nil {
		return 0, err
	}
	var raw [8]byte
	if _, err := io.ReadFull(r.file, raw[8-size:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(raw[:]), nil
}

func (r *ebmlFileReader) readBinaryID(element ebmlElement) (uint64, error) {
	size := element.end - element.data
	if size < 1 || size > 8 {
		return 0, fmt.Errorf("invalid EBML ID size %d", size)
	}
	if _, err := r.file.Seek(element.data, io.SeekStart); err != nil {
		return 0, err
	}
	var raw [8]byte
	if _, err := io.ReadFull(r.file, raw[8-size:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(raw[:]), nil
}

func (r *ebmlFileReader) readFloat(element ebmlElement) (float64, error) {
	size := element.end - element.data
	if _, err := r.file.Seek(element.data, io.SeekStart); err != nil {
		return 0, err
	}
	switch size {
	case 4:
		var raw [4]byte
		if _, err := io.ReadFull(r.file, raw[:]); err != nil {
			return 0, err
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(raw[:]))), nil
	case 8:
		var raw [8]byte
		if _, err := io.ReadFull(r.file, raw[:]); err != nil {
			return 0, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(raw[:])), nil
	default:
		return 0, fmt.Errorf("invalid float size %d", size)
	}
}
