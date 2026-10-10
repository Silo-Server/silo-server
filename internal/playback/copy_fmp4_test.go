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
	group := testGroup(false)
	// 0.5s: 6400 video ticks, 24000 audio ticks.
	if err := shiftFMP4Decode(group, 0.5, scales); err != nil {
		t.Fatal(err)
	}
	if pts, _ := fmp4FirstVideoPTS(group, 1); pts != 1512+6400 {
		t.Fatalf("shifted video PTS = %d, want %d", pts, 1512+6400)
	}
	if pts, _ := fmp4FirstVideoPTS(group, 2); pts != 3744+24000 {
		t.Fatalf("shifted audio decode = %d, want %d", pts, 3744+24000)
	}
	if err := shiftFMP4Decode(testGroup(false), -1, scales); !errors.Is(err, errFMP4Box) {
		t.Fatalf("shift below zero err = %v, want errFMP4Box", err)
	}
}
