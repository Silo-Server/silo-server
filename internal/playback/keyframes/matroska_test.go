package keyframes

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"testing"
)

// el encodes one EBML element with an 8-byte size, which every reader must
// accept.
func el(id uint64, parts ...[]byte) []byte {
	data := bytes.Join(parts, nil)
	var out []byte
	switch {
	case id > 0xFFFFFF:
		out = append(out, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFFFF:
		out = append(out, byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFF:
		out = append(out, byte(id>>8), byte(id))
	default:
		out = append(out, byte(id))
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	size[0] = 0x01
	return append(append(out, size[:]...), data...)
}

func uintEl(id, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return el(id, b[:])
}

func floatEl(id uint64, v float64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(v))
	return el(id, b[:])
}

func cuePoint(ticks, track uint64) []byte {
	return el(idCuePoint, uintEl(idCueTime, ticks), el(idCueTrackPos, uintEl(idCueTrack, track)))
}

var (
	ebmlHeader = el(idEBML, el(0x4282, []byte("matroska")))
	info       = el(idInfo, uintEl(idTimestampScale, 1_000_000), floatEl(idDuration, 12_500))
	tracks     = el(idTracks,
		el(idTrackEntry, uintEl(idTrackNumber, 1), uintEl(idTrackType, 2)), // audio
		el(idTrackEntry, uintEl(idTrackNumber, 2), uintEl(idTrackType, trackTypeVideo)),
	)
	cues = el(idCues,
		cuePoint(4_218, 2),
		cuePoint(0, 2),
		cuePoint(3_000, 1), // audio track: not a video keyframe
		cuePoint(2_216, 2),
		cuePoint(2_216, 2), // duplicate
	)
	cluster = el(idCluster, uintEl(0xE7, 0), bytes.Repeat([]byte{0xAA}, 64))
)

func wantIndex(t *testing.T, got Index, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{0, 2.216, 4.218}; !slices.Equal(got.Keyframes, want) {
		t.Fatalf("keyframes = %v, want %v", got.Keyframes, want)
	}
	if got.Duration != 12.5 {
		t.Fatalf("duration = %v, want 12.5", got.Duration)
	}
}

func read(file []byte) (Index, error) {
	return ReadMatroska(bytes.NewReader(file), int64(len(file)))
}

func TestReadMatroskaCuesBeforeClusters(t *testing.T) {
	file := slices.Concat(ebmlHeader, el(idSegment, info, tracks, cues, cluster))
	got, err := read(file)
	wantIndex(t, got, err)
}

// Most muxers write the Cues after the clusters and point to them from the
// seek head; the reader follows it instead of reading the clusters.
func TestReadMatroskaCuesAfterClustersThroughSeekHead(t *testing.T) {
	// The seek position is relative to the segment's data. The seek head's
	// size doesn't depend on the position's value (8-byte integer), so build
	// it once to measure, then with the real position.
	seekHead := func(pos uint64) []byte {
		return el(idSeekHead, el(idSeek, uintEl(idSeekID, idCues), uintEl(idSeekPosition, pos)))
	}
	before := len(seekHead(0)) + len(info) + len(tracks) + len(cluster)
	segment := el(idSegment, seekHead(uint64(before)), info, tracks, cluster, cues)
	got, err := read(slices.Concat(ebmlHeader, segment))
	wantIndex(t, got, err)
}

// Info may come after the clusters too. Its timestamp scale, here 2ms per
// tick, must still convert the cue times.
func TestReadMatroskaInfoAfterClustersThroughSeekHead(t *testing.T) {
	scaledInfo := el(idInfo, uintEl(idTimestampScale, 2_000_000), floatEl(idDuration, 6_250))
	seekHead := func(cuesPos, infoPos uint64) []byte {
		return el(idSeekHead,
			el(idSeek, uintEl(idSeekID, idCues), uintEl(idSeekPosition, cuesPos)),
			el(idSeek, uintEl(idSeekID, idInfo), uintEl(idSeekPosition, infoPos)))
	}
	cuesPos := len(seekHead(0, 0)) + len(tracks) + len(cluster)
	infoPos := cuesPos + len(cues)
	segment := el(idSegment, seekHead(uint64(cuesPos), uint64(infoPos)), tracks, cluster, cues, scaledInfo)
	got, err := read(slices.Concat(ebmlHeader, segment))
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{0, 4.432, 8.436}; !slices.Equal(got.Keyframes, want) || got.Duration != 12.5 {
		t.Fatalf("keyframes %v, duration %v; want %v and 12.5", got.Keyframes, got.Duration, want)
	}
}

// Tracks may come after the clusters too, found through the seek head.
func TestReadMatroskaTracksAfterClustersThroughSeekHead(t *testing.T) {
	seekHead := func(cuesPos, tracksPos uint64) []byte {
		return el(idSeekHead,
			el(idSeek, uintEl(idSeekID, idCues), uintEl(idSeekPosition, cuesPos)),
			el(idSeek, uintEl(idSeekID, idTracks), uintEl(idSeekPosition, tracksPos)))
	}
	cuesPos := len(seekHead(0, 0)) + len(info) + len(cluster)
	tracksPos := cuesPos + len(cues)
	segment := el(idSegment, seekHead(uint64(cuesPos), uint64(tracksPos)), info, cluster, cues, tracks)
	got, err := read(slices.Concat(ebmlHeader, segment))
	wantIndex(t, got, err)
}

// Malformed data in a cue point makes the index incomplete: the points read
// around it would leave out the keyframe it indexes.
func TestReadMatroskaRejectsMalformedCues(t *testing.T) {
	truncatedPoint := cuePoint(2_216, 2)
	truncatedPoint = truncatedPoint[:len(truncatedPoint)-3]
	// A whole cue point whose track position holds a cut-off CueTrack.
	truncatedTrack := el(idCuePoint, uintEl(idCueTime, 2_216), el(idCueTrackPos, uintEl(idCueTrack, 2)[:5]))
	for name, malformed := range map[string][]byte{
		"truncated cue point":      el(idCues, cuePoint(0, 2), cuePoint(4_218, 2), truncatedPoint),
		"truncated track position": el(idCues, cuePoint(0, 2), truncatedTrack, cuePoint(4_218, 2)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := read(slices.Concat(ebmlHeader, el(idSegment, info, tracks, malformed, cluster)))
			if !errors.Is(err, ErrNoIndex) {
				t.Fatalf("err = %v, want ErrNoIndex", err)
			}
		})
	}
}

func TestReadMatroskaUnknownSizeSegment(t *testing.T) {
	segment := []byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	file := bytes.Join([][]byte{ebmlHeader, segment, info, tracks, cues}, nil)
	got, err := read(file)
	wantIndex(t, got, err)
}

func TestReadMatroskaWithoutIndex(t *testing.T) {
	for name, segment := range map[string][]byte{
		"no cues":            el(idSegment, info, tracks, cluster),
		"no video cue":       el(idSegment, info, tracks, el(idCues, cuePoint(0, 1))),
		"no video track":     el(idSegment, info, el(idTracks, el(idTrackEntry, uintEl(idTrackNumber, 1), uintEl(idTrackType, 2))), cues),
		"no tracks, no cues": el(idSegment, info, cluster),
		// Without Info the cue times can't be converted.
		"no info": el(idSegment, tracks, cues, cluster),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := read(slices.Concat(ebmlHeader, segment))
			if !errors.Is(err, ErrNoIndex) {
				t.Fatalf("err = %v, want ErrNoIndex", err)
			}
		})
	}
}

func TestReadMatroskaRejectsOtherFiles(t *testing.T) {
	for name, file := range map[string][]byte{
		"empty":     nil,
		"mp4":       []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"),
		"truncated": ebmlHeader[:5],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := read(file); err == nil {
				t.Fatal("read succeeded, want an error")
			}
		})
	}
}

// A Cues element larger than the reader loads is a damaged file, not a
// multi-gigabyte allocation.
func TestReadMatroskaRejectsOversizedCues(t *testing.T) {
	huge := []byte{0x1C, 0x53, 0xBB, 0x6B, 0x01, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00}
	// An unknown-size segment, so the reader reaches the Cues header.
	file := bytes.Join([][]byte{
		ebmlHeader,
		{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		info, tracks, huge,
	}, nil)
	if _, err := read(file); err == nil || errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want a size error", err)
	}
}
