package embeddedsub

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"testing"
	"time"
)

func be32(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.BigEndian.AppendUint32(b, x)
	}
	return b
}

// textSample is a tx3g sample: a 16-bit length and the text.
func textSample(s string) []byte {
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(s))), s...)
}

// testMP4Track is a 3GPP text track: movie timescale 1000, media timescale
// one microsecond, English, an edit list that starts it 2 s in, and a
// sample description whose display flags mark every sample forced.
func testMP4Track(elst []byte) *mp4Track {
	matrix := be32(0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000)
	mvhd := slices.Concat(be32(0, 0, 0, 1000, 60_000, 0x10000), []byte{1, 0}, make([]byte, 10), matrix, make([]byte, 24), be32(8))
	tkhd := slices.Concat(be32(3, 0, 0, 7, 0, 60_000, 0, 0), []byte{0, 0, 0, 0, 0, 0, 0, 0}, matrix, be32(0, 0))
	mdhd := slices.Concat(be32(0, 0, 0, 1_000_000, 60_000_000), []byte{0x15, 0xC7, 0, 0})
	hdlr := slices.Concat(be32(0, 0), []byte("sbtl"), make([]byte, 12), []byte("SubtitleHandler\x00"))
	tx3g := slices.Concat(make([]byte, 6), []byte{0, 1}, be32(0x80000000), []byte{1, 0xFF}, be32(0),
		be32(0, 0), []byte{0, 0, 0, 0, 0, 1, 0, 18}, be32(0xFFFFFFFF), mp4Box("ftab", []byte{0, 1, 0, 1, 5}, []byte("Serif")))
	m := &mp4Track{
		ftyp: mp4Box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41")),
		mvhd: mp4Box("mvhd", mvhd), tkhd: mp4Box("tkhd", tkhd), mdhd: mp4Box("mdhd", mdhd), hdlr: mp4Box("hdlr", hdlr),
		stsd: mp4FullBox("stsd", slices.Concat(be32(1), mp4Box("tx3g", tx3g))),
	}
	if elst != nil {
		m.edts = mp4Box("edts", mp4FullBox("elst", elst))
	}
	return m
}

func TestMP4RoundTrip(t *testing.T) {
	// An empty edit of 2 s, then the media from its start.
	m := testMP4Track(be32(2, 2000, 0xFFFFFFFF, 0x10000, 60_000, 0, 0x10000))
	packets := []Packet{
		{Data: textSample("one"), mp4DTS: 500_000, mp4Delta: 1_000_000},
		{Data: textSample("two {\\an8}"), mp4DTS: 1_500_000, mp4Delta: 500_000},
		{Data: textSample("three"), mp4DTS: 5_000_000, mp4Delta: 2_000_000},
	}
	var file bytes.Buffer
	if err := writeMP4(&file, &Track{mp4: m}, packets); err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, file.Bytes())
	ctx := context.Background()
	idx, err := ReadIndex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := idx.Track(7)
	// The gaps before "one" and "three" become empty samples.
	if !ok || tr.CodecID != "tx3g" || tr.Language != "eng" || !tr.Forced || !tr.Enabled ||
		tr.Completeness != CompletenessComplete || tr.FrameCount != 5 || tr.Unsupported != "" {
		t.Fatalf("track = %+v", tr)
	}
	text := func(ps []Packet) []wantPacket {
		var out []wantPacket
		for _, p := range ps {
			if len(p.Data) > 2 {
				out = append(out, wantPacket{p.PTS, p.Duration, string(p.Data[2:])})
			}
		}
		return out
	}
	res, err := ReadPackets(ctx, path, idx, 7, WholeFrom, WholeTo, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []wantPacket{{ms(2500), time.Second, "one"}, {ms(3500), ms(500), "two {\\an8}"}, {ms(7000), 2 * time.Second, "three"}}
	if got := text(res.Packets); !slices.Equal(got, want) {
		t.Fatalf("packets:\n got %+v\nwant %+v", got, want)
	}
	if len(res.Packets) != 5 || !HasOverrideTags(res.Packets) {
		t.Fatalf("%d packets", len(res.Packets))
	}
	// Neighboring samples come in one read.
	if res.Stats.Reads != 1 {
		t.Fatalf("whole track took %d reads", res.Stats.Reads)
	}
	res, err = ReadPackets(ctx, path, idx, 7, ms(4100), ms(6900), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(res.Packets); len(got) != 0 {
		t.Fatalf("gap window = %+v", got)
	}
	res, err = ReadPackets(ctx, path, idx, 7, ms(3000), ms(3600), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(res.Packets); !slices.Equal(got, want[:2]) {
		t.Fatalf("window = %+v", got)
	}

	// A window's packets written out keep their decode times.
	var again bytes.Buffer
	if err := writeMP4(&again, &Track{mp4: m}, res.Packets[len(res.Packets)-1:]); err != nil {
		t.Fatal(err)
	}
	path = writeFixture(t, again.Bytes())
	if idx, err = ReadIndex(ctx, path); err != nil {
		t.Fatal(err)
	}
	if res, err = ReadPackets(ctx, path, idx, 7, WholeFrom, WholeTo, ReadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := text(res.Packets); !slices.Equal(got, want[1:2]) {
		t.Fatalf("rewritten window = %+v", got)
	}
}

func TestMP4TracksTheReaderRefuses(t *testing.T) {
	tests := map[string]struct {
		elst, stsd  []byte
		unsupported string
	}{
		"edit at double speed": {elst: be32(1, 60_000, 0, 0x20000), unsupported: unsupportedEditList},
		"two media edits":      {elst: be32(2, 1000, 0, 0x10000, 1000, 5000, 0x10000), unsupported: unsupportedEditList},
		"WebVTT in MP4":        {stsd: mp4FullBox("stsd", slices.Concat(be32(1), mp4Box("wvtt", make([]byte, 8)))), unsupported: `MP4 sample entry "wvtt"`},
	}
	for name, tt := range tests {
		m := testMP4Track(tt.elst)
		if tt.stsd != nil {
			m.stsd = tt.stsd
		}
		var file bytes.Buffer
		if err := writeMP4(&file, &Track{mp4: m}, []Packet{{Data: textSample("x"), mp4Delta: 1000}}); err != nil {
			t.Fatal(err)
		}
		idx, err := ReadIndex(context.Background(), writeFixture(t, file.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if tr, ok := idx.Track(7); !ok || tr.Unsupported != tt.unsupported {
			t.Errorf("%s: track = %+v", name, tr)
		}
	}
}
