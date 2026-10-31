package playback

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func box(typ string, parts ...[]byte) []byte {
	payload := bytes.Join(parts, nil)
	out := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:], typ)
	return append(out, payload...)
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// fullBox prefixes a version and flags.
func fullBox(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	return box(typ, append([]byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}, bytes.Join(parts, nil)...))
}

// testInit has video track 1 at 12800 and audio track 2 at 48000.
func testInit() []byte {
	trak := func(id, scale uint32, handler string) []byte {
		return box("trak",
			fullBox("tkhd", 0, 3, u32(0), u32(0), u32(id)),
			box("mdia",
				fullBox("mdhd", 0, 0, u32(0), u32(0), u32(scale)),
				fullBox("hdlr", 0, 0, u32(0), []byte(handler)),
			),
		)
	}
	return bytes.Join([][]byte{
		box("ftyp", []byte("isom")),
		box("moov", trak(2, 48000, "soun"), trak(1, 12800, "vide")),
	}, nil)
}

// testGroup is one keyframe group: video starting at decode 1000 with a
// first-sample composition offset of 512, audio at decode 3744.
func testGroup(withStyp bool) []byte {
	videoTrun := fullBox("trun", 0, 0xb05, u32(1), u32(0), u32(0), u32(40), u32(100), u32(512))
	moof := box("moof",
		fullBox("mfhd", 0, 0, u32(1)),
		box("traf", fullBox("tfhd", 0, 0x20000, u32(1)), fullBox("tfdt", 1, 0, u64(1000)), videoTrun),
		box("traf", fullBox("tfhd", 0, 0x20000, u32(2)), fullBox("tfdt", 1, 0, u64(3744)), fullBox("trun", 0, 1, u32(0), u32(0))),
	)
	parts := [][]byte{box("sidx", u32(0)), moof, box("mdat", []byte{1, 2, 3})}
	if withStyp {
		parts = append([][]byte{box("styp", []byte("msdh"))}, parts...)
	}
	return bytes.Join(parts, nil)
}

func TestFMP4VideoTrackAndFirstPTS(t *testing.T) {
	id, scale, err := fmp4VideoTrack(testInit())
	if err != nil || id != 1 || scale != 12800 {
		t.Fatalf("video track = %d at %d, %v; want 1 at 12800", id, scale, err)
	}
	pts, err := fmp4FirstVideoPTS(testGroup(true), 1)
	if err != nil || pts != 1512 {
		t.Fatalf("first video PTS = %d, %v; want decode 1000 + offset 512", pts, err)
	}
	if _, err := fmp4FirstVideoPTS(testGroup(true), 9); !errors.Is(err, errFMP4Box) {
		t.Fatalf("unknown track err = %v, want errFMP4Box", err)
	}
}

func TestJoinFMP4GroupsKeepsOneStypAndNoSidx(t *testing.T) {
	joined, err := joinFMP4Groups([][]byte{testGroup(true), testGroup(true)})
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := fmp4Boxes(joined)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, b := range boxes {
		types = append(types, b.typ)
	}
	if got := bytes.Join(func() [][]byte {
		out := make([][]byte, len(types))
		for i, typ := range types {
			out[i] = []byte(typ)
		}
		return out
	}(), []byte(",")); string(got) != "styp,moof,mdat,moof,mdat" {
		t.Fatalf("joined boxes = %s, want styp,moof,mdat,moof,mdat", got)
	}
}

func TestShiftFMP4DecodeMovesEveryTrack(t *testing.T) {
	scales, err := fmp4TrackTimescales(testInit())
	if err != nil {
		t.Fatal(err)
	}
	// 0.5s: 6400 video ticks, 24000 audio ticks.
	group, err := shiftFMP4Decode(testGroup(false), 0.5, scales, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pts, _ := fmp4FirstVideoPTS(group, 1); pts != 1512+6400 {
		t.Fatalf("shifted video PTS = %d, want %d", pts, 1512+6400)
	}
	if pts, _ := fmp4FirstVideoPTS(group, 2); pts != 3744+24000 {
		t.Fatalf("shifted audio decode = %d, want %d", pts, 3744+24000)
	}
	if _, err := shiftFMP4Decode(testGroup(false), -1, scales, 1); !errors.Is(err, errFMP4Box) {
		t.Fatalf("video shift below zero err = %v, want errFMP4Box", err)
	}
}

// An audio fragment shifted before zero, an encoder's priming, loses the
// samples before zero; the rest keep their place on the timeline.
func TestShiftFMP4DecodeDropsAudioBeforeZero(t *testing.T) {
	scales, err := fmp4TrackTimescales(testInit())
	if err != nil {
		t.Fatal(err)
	}
	// Audio: three 1024-tick samples of 10, 20 and 30 bytes from decode 0;
	// video at decode 1000. Data offsets count from the moof.
	group := func() []byte {
		return bytes.Join([][]byte{
			box("moof",
				fullBox("mfhd", 0, 0, u32(1)),
				box("traf", fullBox("tfhd", 0, 0x20000, u32(1)), fullBox("tfdt", 1, 0, u64(1000)),
					fullBox("trun", 0, 0x201, u32(1), u32(500), u32(40))),
				box("traf", fullBox("tfhd", 0, 0x20008, u32(2), u32(1024)), fullBox("tfdt", 1, 0, u64(0)),
					fullBox("trun", 0, 0x201, u32(3), u32(600), u32(10), u32(20), u32(30))),
			),
			box("mdat", make([]byte, 100)),
		}, nil)
	}
	// -0.021375s, AAC priming: -1026 audio ticks, -274 video ticks.
	out, err := shiftFMP4Decode(group(), -0.021375, scales, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(group())-8 {
		t.Fatalf("segment is %d bytes, want %d: two 4-byte trun entries fewer", len(out), len(group())-8)
	}
	top, err := fmp4Boxes(out)
	if err != nil || top[0].typ != boxMoof {
		t.Fatalf("boxes = %v, %v", top, err)
	}
	trafs, err := fmp4Boxes(top[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	read := func(traf []byte) (uint64, []byte) {
		tfdt, _ := fmp4Child(traf, "tfdt")
		trun, _ := fmp4Child(traf, "trun")
		return binary.BigEndian.Uint64(tfdt[4:]), trun
	}
	decode, trun := read(trafs[1].payload)
	if offset := binary.BigEndian.Uint32(trun[8:]); decode != 1000-274 || offset != 500-8 {
		t.Fatalf("video decode %d, data offset %d; want %d, %d", decode, offset, 1000-274, 500-8)
	}
	decode, trun = read(trafs[2].payload)
	// Two samples go: the second still starts 2 ticks before zero.
	if count, size := binary.BigEndian.Uint32(trun[4:]), binary.BigEndian.Uint32(trun[12:]); decode != 1022 || count != 1 || size != 30 {
		t.Fatalf("audio decode %d, %d samples, first of %d bytes; want 1022, 1, 30", decode, count, size)
	}
	if offset := binary.BigEndian.Uint32(trun[8:]); offset != 600+10+20-8 {
		t.Fatalf("audio data offset %d, want %d: past the dropped media, nearer by the shorter moof", offset, 600+10+20-8)
	}

	// An audio fragment wholly before zero has nothing to keep: -0.07s ends
	// its 3072 ticks 288 before zero, while the video stays after it.
	if _, err := shiftFMP4Decode(group(), -0.07, scales, 1); !errors.Is(err, errFMP4Box) {
		t.Fatalf("whole-fragment shift err = %v, want errFMP4Box", err)
	}
}
