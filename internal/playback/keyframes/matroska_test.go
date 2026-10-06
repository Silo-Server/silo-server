package keyframes

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"testing"
)

// el encodes one EBML element with an 8-byte size, so an element's length
// never depends on its values and cluster positions can be computed up front.
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

const (
	audioTrack = 1
	videoTrack = 2
)

// block encodes a SimpleBlock at a timestamp relative to its cluster.
func block(track uint64, rel int16, key bool) []byte {
	var flags byte
	if key {
		flags = 0x80
	}
	var ts [2]byte
	binary.BigEndian.PutUint16(ts[:], uint16(rel))
	return el(idSimpleBlock, []byte{0x80 | byte(track)}, ts[:], []byte{flags}, bytes.Repeat([]byte{0xAA}, 16))
}

// groupBlock encodes a BlockGroup; a referencing block is not a keyframe.
func groupBlock(track uint64, rel int16, references bool) []byte {
	var ts [2]byte
	binary.BigEndian.PutUint16(ts[:], uint16(rel))
	parts := [][]byte{el(idBlock, []byte{0x80 | byte(track)}, ts[:], []byte{0}, bytes.Repeat([]byte{0xBB}, 16))}
	if references {
		parts = append(parts, uintEl(idReferenceBlock, 40))
	}
	return el(idBlockGroup, parts...)
}

func cluster(ticks uint64, blocks ...[]byte) []byte {
	return el(idCluster, append([][]byte{uintEl(idClusterTimestamp, ticks)}, blocks...)...)
}

var (
	ebmlHeader = el(idEBML, el(0x4282, []byte("matroska")))
	info       = el(idInfo, uintEl(idTimestampScale, 1_000_000), floatEl(idDuration, 12_500))
	tracks     = el(idTracks,
		el(idTrackEntry, uintEl(idTrackNumber, audioTrack), uintEl(idTrackType, 2)),
		el(idTrackEntry, uintEl(idTrackNumber, videoTrack), uintEl(idTrackType, trackTypeVideo)),
	)
	// Video keyframes at 0, 2.216s (a BlockGroup) and 4.218s; non-keyframes
	// and audio between them.
	clusterA = cluster(0,
		block(videoTrack, 0, true),
		block(audioTrack, 10, true),
		block(videoTrack, 40, false),
		groupBlock(videoTrack, 80, true),
		groupBlock(videoTrack, 2216, false),
	)
	clusterB = cluster(4218,
		block(videoTrack, 0, true),
		block(videoTrack, 40, false),
	)
)

type layout struct {
	cuesFirst   bool    // Cues before the clusters, found by the linear scan
	unknownSize bool    // a live-style segment with no size
	dropCue     *uint64 // a video keyframe left out of the Cues
	noPositions bool
}

// file builds a Matroska file whose Cues index the fixture's keyframes, plus
// an audio cue and a duplicate, pointing at the clusters that hold them.
func file(l layout) []byte {
	seekHead := func(pos uint64) []byte {
		return el(idSeekHead, el(idSeek, uintEl(idSeekID, idCues), uintEl(idSeekPosition, pos)))
	}
	head := slices.Concat(seekHead(0), info, tracks)
	cuesLen := len(cuesFor(l, 0, 0))
	posA := uint64(len(head))
	if l.cuesFirst {
		posA += uint64(cuesLen)
	}
	posB := posA + uint64(len(clusterA))
	cues := cuesFor(l, posA, posB)

	var body []byte
	if l.cuesFirst {
		body = slices.Concat(seekHead(uint64(len(head))), info, tracks, cues, clusterA, clusterB)
	} else {
		body = slices.Concat(seekHead(posB+uint64(len(clusterB))), info, tracks, clusterA, clusterB, cues)
	}
	if l.unknownSize {
		return slices.Concat(ebmlHeader, []byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, body)
	}
	return slices.Concat(ebmlHeader, el(idSegment, body))
}

func cuesFor(l layout, posA, posB uint64) []byte {
	point := func(ticks, track, pos uint64) []byte {
		if l.dropCue != nil && ticks == *l.dropCue && track == videoTrack {
			return nil
		}
		trackPos := [][]byte{uintEl(idCueTrack, track)}
		if !l.noPositions {
			trackPos = append(trackPos, uintEl(idCueClusterPosition, pos))
		}
		return el(idCuePoint, uintEl(idCueTime, ticks), el(idCueTrackPos, trackPos...))
	}
	return el(idCues,
		point(4218, videoTrack, posB),
		point(0, videoTrack, posA),
		point(10, audioTrack, posA), // audio: not a video keyframe
		point(2216, videoTrack, posA),
		point(2216, videoTrack, posA), // duplicate
	)
}

func ptr[T any](v T) *T { return &v }

func read(f []byte) (Index, error) {
	return ReadMatroska(bytes.NewReader(f), int64(len(f)))
}

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

func TestReadMatroskaCuesBeforeClusters(t *testing.T) {
	got, err := read(file(layout{cuesFirst: true}))
	wantIndex(t, got, err)
}

// Most muxers write the Cues after the clusters and point to them from the
// seek head; the reader follows it instead of reading the clusters.
func TestReadMatroskaCuesAfterClustersThroughSeekHead(t *testing.T) {
	got, err := read(file(layout{}))
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
	posA := uint64(len(seekHead(0, 0)) + len(tracks))
	posB := posA + uint64(len(clusterA))
	cues := cuesFor(layout{}, posA, posB)
	cuesPos := posB + uint64(len(clusterB))
	infoPos := cuesPos + uint64(len(cues))
	segment := el(idSegment, seekHead(cuesPos, infoPos), tracks, clusterA, clusterB, cues, scaledInfo)
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
	posA := uint64(len(seekHead(0, 0)) + len(info))
	posB := posA + uint64(len(clusterA))
	cues := cuesFor(layout{}, posA, posB)
	cuesPos := posB + uint64(len(clusterB))
	tracksPos := cuesPos + uint64(len(cues))
	segment := el(idSegment, seekHead(cuesPos, tracksPos), info, clusterA, clusterB, cues, tracks)
	got, err := read(slices.Concat(ebmlHeader, segment))
	wantIndex(t, got, err)
}

// A malformed cue point makes the index incomplete, even when the points read
// before it happen to cover every keyframe: here the broken one is the last,
// a duplicate.
func TestReadMatroskaRejectsMalformedCues(t *testing.T) {
	seekHead := func(pos uint64) []byte {
		return el(idSeekHead, el(idSeek, uintEl(idSeekID, idCues), uintEl(idSeekPosition, pos)))
	}
	posA := uint64(len(seekHead(0)) + len(info) + len(tracks))
	posB := posA + uint64(len(clusterA))
	cues := cuesFor(layout{}, posA, posB)
	payload := cues[12:] // past the Cues ID and its 8-byte size
	malformed := el(idCues, payload[:len(payload)-3])
	segment := el(idSegment, seekHead(posB+uint64(len(clusterB))), info, tracks, clusterA, clusterB, malformed)
	_, err := read(slices.Concat(ebmlHeader, segment))
	if !errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want ErrNoIndex", err)
	}
}

func TestReadMatroskaUnknownSizeSegment(t *testing.T) {
	got, err := read(file(layout{unknownSize: true}))
	wantIndex(t, got, err)
}

// A plan built on Cues that skip keyframes would cut segments where FFmpeg
// doesn't, so such a file has no usable index.
func TestReadMatroskaRejectsCuesMissingKeyframes(t *testing.T) {
	for name, l := range map[string]layout{
		"keyframe without a cue (Block)":       {dropCue: ptr(uint64(2216))},
		"keyframe without a cue (SimpleBlock)": {dropCue: ptr(uint64(4218)), cuesFirst: true},
		"cues without cluster positions":       {noPositions: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := read(file(l))
			if !errors.Is(err, ErrIncompleteIndex) || !errors.Is(err, ErrNoIndex) {
				t.Fatalf("err = %v, want ErrIncompleteIndex (an ErrNoIndex)", err)
			}
		})
	}
}

func TestReadMatroskaWithoutIndex(t *testing.T) {
	audioOnlyTracks := el(idTracks, el(idTrackEntry, uintEl(idTrackNumber, audioTrack), uintEl(idTrackType, 2)))
	for name, segment := range map[string][]byte{
		"no cues":        el(idSegment, info, tracks, clusterA),
		"no video cue":   el(idSegment, info, tracks, el(idCues, el(idCuePoint, uintEl(idCueTime, 0), el(idCueTrackPos, uintEl(idCueTrack, audioTrack))))),
		"no video track": el(idSegment, info, audioOnlyTracks, clusterA),
		// Without Info the cue times can't be converted.
		"no info": el(idSegment, tracks, cuesFor(layout{}, 0, 0), clusterA),
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
	for name, f := range map[string][]byte{
		"empty":     nil,
		"mp4":       []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"),
		"truncated": ebmlHeader[:5],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := read(f); err == nil {
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
	f := slices.Concat(
		ebmlHeader,
		[]byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		info, tracks, huge,
	)
	if _, err := read(f); err == nil || errors.Is(err, ErrNoIndex) {
		t.Fatalf("err = %v, want a size error", err)
	}
}

// manyClusters builds a file with ten clusters, each holding two video
// keyframes, and Cues for all of them except dropTicks. The sampled check
// reads clusters 0, 3, 6 and 9 and the ones after them; cluster 2 is only
// read by the full check.
func manyClusters(dropTicks uint64, extraCue uint64) []byte {
	head := slices.Concat(info, tracks)
	var clusters [][]byte
	for c := range 10 {
		base := uint64(c) * 4000
		clusters = append(clusters, cluster(base, block(videoTrack, 0, true), block(videoTrack, 40, false), block(videoTrack, 2000, true)))
	}
	cuesFor := func(positions []uint64) []byte {
		var points [][]byte
		point := func(ticks, pos uint64) []byte {
			return el(idCuePoint, uintEl(idCueTime, ticks),
				el(idCueTrackPos, uintEl(idCueTrack, videoTrack), uintEl(idCueClusterPosition, pos)))
		}
		for c := range 10 {
			for _, rel := range []uint64{0, 2000} {
				if ticks := uint64(c)*4000 + rel; ticks != dropTicks {
					points = append(points, point(ticks, positions[c]))
				}
			}
		}
		if extraCue != 0 {
			points = append(points, point(extraCue, positions[extraCue/4000]))
		}
		return el(idCues, points...)
	}
	// The Cues come before the clusters; their length doesn't depend on the
	// positions they hold.
	positions := make([]uint64, 10)
	pos := uint64(len(head) + len(cuesFor(positions)))
	for c, cl := range clusters {
		positions[c] = pos
		pos += uint64(len(cl))
	}
	body := slices.Concat(head, cuesFor(positions), slices.Concat(clusters...))
	return slices.Concat(ebmlHeader, el(idSegment, body))
}

func verify(f []byte) (Index, error) {
	return VerifyMatroska(bytes.NewReader(f), int64(len(f)))
}

func TestVerifyMatroskaChecksEveryCluster(t *testing.T) {
	complete := manyClusters(1<<62, 0)
	if idx, err := verify(complete); err != nil || len(idx.Keyframes) != 20 {
		t.Fatalf("complete cues: %d keyframes, %v; want 20 and no error", len(idx.Keyframes), err)
	}

	// A keyframe in cluster 2 without a cue gets past the sampled check but
	// not the full one.
	missing := manyClusters(2*4000+2000, 0)
	if _, err := read(missing); err != nil {
		t.Fatalf("sampled check: %v, want it to miss cluster 2", err)
	}
	if _, err := verify(missing); !errors.Is(err, ErrIncompleteIndex) {
		t.Fatalf("full check err = %v, want ErrIncompleteIndex", err)
	}

	// A cue on a frame that isn't a keyframe would plan a boundary FFmpeg
	// can't cut at.
	if _, err := verify(manyClusters(1<<62, 2*4000+40)); !errors.Is(err, ErrIncompleteIndex) {
		t.Fatalf("cue on a non-keyframe err = %v, want ErrIncompleteIndex", err)
	}
}

func TestVideoEndSeconds(t *testing.T) {
	var end videoEnd
	if got := end.seconds(defaultTimescale, 0.04); got != 0 {
		t.Fatalf("no frames: %v, want 0", got)
	}
	end.add(0, 0)
	if got := end.seconds(defaultTimescale, 0); got != 0 {
		t.Fatalf("one frame without a frame duration: %v, want 0", got)
	}
	// Presentation order differs from storage order with B-frames.
	for _, ms := range []int64{120, 80, 40, 100, 120} {
		end.add(ms, 0)
	}
	if got := end.seconds(defaultTimescale, 0); math.Abs(got-0.14) > 1e-9 {
		t.Fatalf("from the last gap: %v, want 0.14", got)
	}
	if got := end.seconds(defaultTimescale, 0.033); math.Abs(got-0.153) > 1e-9 {
		t.Fatalf("from the frame duration: %v, want 0.153", got)
	}
	// The last frame's own BlockDuration wins over both.
	end.add(120, 70)
	if got := end.seconds(defaultTimescale, 0.033); math.Abs(got-0.19) > 1e-9 {
		t.Fatalf("from the block duration: %v, want 0.19", got)
	}
	// Blocks sharing the last timestamp end with the longest, in either order.
	for _, durations := range [][2]int64{{70, 30}, {30, 70}} {
		var shared videoEnd
		shared.add(80, 0)
		shared.add(120, durations[0])
		shared.add(120, durations[1])
		if got := shared.seconds(defaultTimescale, 0.033); math.Abs(got-0.19) > 1e-9 {
			t.Fatalf("blocks of %v ms at the end: %v, want 0.19", durations, got)
		}
	}
	// A later frame without one goes back to the track's duration.
	end.add(160, 0)
	if got := end.seconds(defaultTimescale, 0.033); math.Abs(got-0.193) > 1e-9 {
		t.Fatalf("after a later frame: %v, want 0.193", got)
	}
}

func TestReadBlockGroupDuration(t *testing.T) {
	var ts [2]byte
	binary.BigEndian.PutUint16(ts[:], 40)
	group := el(idBlockGroup,
		el(idBlock, []byte{0x80 | byte(videoTrack)}, ts[:], []byte{0}, bytes.Repeat([]byte{0xBB}, 16)),
		uintEl(idBlockDuration, 70))
	r := bytes.NewReader(group)
	_, size, headerLen, err := readElementHeader(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	track, rel, key, duration, err := readBlockGroup(r, headerLen, size)
	if err != nil || track != videoTrack || rel != 40 || !key || duration != 70 {
		t.Fatalf("readBlockGroup = %d, %d, %v, %d, %v; want video at 40, a keyframe of 70 ticks", track, rel, key, duration, err)
	}
}

// The fixture's last video frame is at 4.258s, 40ms after the one before.
func TestVerifyMatroskaVideoEnd(t *testing.T) {
	idx, err := verify(file(layout{}))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(idx.VideoEnd-4.298) > 1e-9 {
		t.Fatalf("VideoEnd = %v, want 4.298", idx.VideoEnd)
	}
}

// A video track whose timestamps carry a TrackTimestampScale isn't planned
// from: the reader's times don't apply it.
func TestFirstVideoTrackReportsATrackTimestampScale(t *testing.T) {
	entry := func(scale ...[]byte) []byte {
		return el(idTrackEntry, append([][]byte{uintEl(idTrackNumber, videoTrack), uintEl(idTrackType, trackTypeVideo)}, scale...)...)
	}
	if _, _, scaled, found := firstVideoTrack(entry()); !found || scaled {
		t.Fatalf("unscaled track: scaled=%v found=%v", scaled, found)
	}
	if _, _, scaled, found := firstVideoTrack(entry(floatEl(idTrackTimestampScale, 1))); !found || scaled {
		t.Fatalf("scale 1: scaled=%v found=%v", scaled, found)
	}
	if _, _, scaled, found := firstVideoTrack(entry(floatEl(idTrackTimestampScale, 2))); !found || !scaled {
		t.Fatalf("scale 2: scaled=%v found=%v", scaled, found)
	}
}
