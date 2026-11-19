package playback

import (
	"bytes"
	"encoding/binary"
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

	// An audio fragment wholly before zero goes: -0.07s ends its 3072 ticks
	// 288 before zero, while the video stays after it.
	audioTraf := len(trafs[2].raw) + 8 // two trun entries fewer than the original
	out, err = shiftFMP4Decode(group(), -0.07, scales, 1)
	if err != nil {
		t.Fatal(err)
	}
	if top, err = fmp4Boxes(out); err != nil || len(top) != 2 || top[0].typ != boxMoof || top[1].typ != "mdat" {
		t.Fatalf("boxes = %v, %v; want the moof and its mdat", top, err)
	}
	if trafs, err = fmp4Boxes(top[0].payload); err != nil || len(trafs) != 2 {
		t.Fatalf("moof children = %v, %v; want mfhd and the video traf", trafs, err)
	}
	decode, trun = read(trafs[1].payload)
	if offset := binary.BigEndian.Uint32(trun[8:]); decode != 1000-896 || offset != uint32(500-audioTraf) {
		t.Fatalf("video decode %d, data offset %d; want %d, %d", decode, offset, 1000-896, 500-audioTraf)
	}

	// A moof with only that audio goes with its mdat.
	audioOnly := bytes.Join([][]byte{
		box("moof", fullBox("mfhd", 0, 0, u32(1)),
			box("traf", fullBox("tfhd", 0, 0x20008, u32(2), u32(1024)), fullBox("tfdt", 1, 0, u64(0)),
				fullBox("trun", 0, 0x201, u32(1), u32(100), u32(10)))),
		box("mdat", make([]byte, 10)),
		box("moof", fullBox("mfhd", 0, 0, u32(2)),
			box("traf", fullBox("tfhd", 0, 0x20000, u32(1)), fullBox("tfdt", 1, 0, u64(1000)),
				fullBox("trun", 0, 0x201, u32(1), u32(100), u32(40)))),
		box("mdat", make([]byte, 40)),
	}, nil)
	if out, err = shiftFMP4Decode(audioOnly, -0.07, scales, 1); err != nil {
		t.Fatal(err)
	}
	if top, err = fmp4Boxes(out); err != nil || len(top) != 2 || len(top[1].payload) != 40 {
		t.Fatalf("boxes = %v, %v; want only the video moof and its mdat", top, err)
	}
}
