package matroska

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"
)

func TestParseElementHeader(t *testing.T) {
	tests := []struct {
		name   string
		b      []byte
		id     uint64
		size   int64
		hdrLen int
		wantOK bool
	}{
		{name: "one-byte id and size", b: []byte{0xE7, 0x81, 0x05}, id: 0xE7, size: 1, hdrLen: 2, wantOK: true},
		{name: "four-byte id, eight-byte size", b: []byte{0x1F, 0x43, 0xB6, 0x75, 0x01, 0, 0, 0, 0, 0, 0x01, 0x00}, id: IDCluster, size: 256, hdrLen: 12, wantOK: true},
		{name: "unknown size", b: []byte{0x18, 0x53, 0x80, 0x67, 0xFF}, id: IDSegment, size: UnknownSize, hdrLen: 5, wantOK: true},
		{name: "unknown size, two bytes", b: []byte{0xA3, 0x7F, 0xFF}, id: IDSimpleBlock, size: UnknownSize, hdrLen: 3, wantOK: true},
		{name: "cut off size", b: []byte{0x1F, 0x43, 0xB6, 0x75, 0x40}, wantOK: false},
		{name: "zero lead byte", b: []byte{0x00, 0x81}, wantOK: false},
		{name: "five-byte id", b: []byte{0x08, 0x01, 0x02, 0x03, 0x04, 0x81}, wantOK: false},
		{name: "empty", b: nil, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, size, hl, ok := ParseElementHeader(tt.b)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && (id != tt.id || size != tt.size || hl != tt.hdrLen) {
				t.Fatalf("got (%#x, %d, %d), want (%#x, %d, %d)", id, size, hl, tt.id, tt.size, tt.hdrLen)
			}
		})
	}
}

func TestReadElementHeaderErrors(t *testing.T) {
	r := bytes.NewReader([]byte{0x1F, 0x43})
	if _, _, _, err := ReadElementHeader(r, 0); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("cut-off header: err = %v, want ErrUnexpectedEOF", err)
	}
	if _, _, _, err := ReadElementHeader(r, 2); !errors.Is(err, io.EOF) {
		t.Fatalf("at the end: err = %v, want EOF", err)
	}
	failing := errors.New("disk gone")
	if _, _, _, err := ReadElementHeader(errReader{failing}, 0); !errors.Is(err, failing) {
		t.Fatalf("failed read: err = %v, want %v", err, failing)
	}
}

type errReader struct{ err error }

func (r errReader) ReadAt([]byte, int64) (int, error) { return 0, r.err }

func TestAppendSizeRoundTrips(t *testing.T) {
	for _, size := range []uint64{0, 1, 126, 127, 128, 16382, 16383, 16384, 1 << 21, 1<<35 + 7} {
		b := AppendSize(nil, size)
		got, n, ok := ReadVint(b, false)
		if !ok || n != len(b) || got != size {
			t.Fatalf("size %d: encoded %x, read back %d (%d bytes, ok %v)", size, b, got, n, ok)
		}
		// All ones is the unknown size, never a real one.
		if _, s, _, _ := ParseElementHeader(append([]byte{0xA3}, b...)); s == UnknownSize {
			t.Fatalf("size %d encoded as unknown: %x", size, b)
		}
	}
}

func TestAppendIntRoundTrips(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 127, 128, -128, -129, 32767, -32768, 1 << 40, -(1 << 40)} {
		b := AppendInt(nil, 0xFB, v)
		_, size, hl, ok := ParseElementHeader(b)
		if !ok {
			t.Fatalf("%d: bad element %x", v, b)
		}
		if got := Int(b[hl : int64(hl)+size]); got != v {
			t.Fatalf("%d: read back %d from %x", v, got, b)
		}
	}
	if got := len(AppendInt(nil, 0xFB, -1)); got != 3 {
		t.Fatalf("-1 takes %d bytes, want 3", got)
	}
}

func TestChildrenCompleteReportsMalformedData(t *testing.T) {
	good := AppendUint(AppendUint(nil, 0xD7, 3), 0x83, 17)
	var ids []uint64
	if !ChildrenComplete(good, func(id uint64, _ []byte) { ids = append(ids, id) }) {
		t.Fatal("well-formed children reported incomplete")
	}
	if !slices.Equal(ids, []uint64{0xD7, 0x83}) {
		t.Fatalf("ids = %x", ids)
	}
	// A child claiming more data than its parent holds.
	bad := append(slices.Clone(good), 0x86, 0x85, 'S')
	ids = nil
	if ChildrenComplete(bad, func(id uint64, _ []byte) { ids = append(ids, id) }) {
		t.Fatal("overlong child reported complete")
	}
	if len(ids) != 2 {
		t.Fatalf("children before the bad one: %x", ids)
	}
}

func TestWalkCuePoints(t *testing.T) {
	// The second point lists its CueTime after its positions.
	cues := AppendElement(nil, IDCuePoint, slices.Concat(
		AppendUint(nil, IDCueTime, 100),
		AppendElement(nil, IDCueTrackPositions, slices.Concat(
			AppendUint(nil, IDCueTrack, 1), AppendUint(nil, IDCueClusterPosition, 4000))),
		AppendElement(nil, IDCueTrackPositions, slices.Concat(
			AppendUint(nil, IDCueTrack, 3), AppendUint(nil, IDCueClusterPosition, 4000),
			AppendUint(nil, IDCueRelativePosition, 77), AppendUint(nil, IDCueDuration, 1500))),
	))
	cues = AppendElement(cues, IDCuePoint, slices.Concat(
		AppendElement(nil, IDCueTrackPositions, slices.Concat(
			AppendUint(nil, IDCueTrack, 3), AppendUint(nil, IDCueClusterPosition, 9000),
			AppendElement(nil, IDCueReference, nil))),
		AppendUint(nil, IDCueTime, 2500),
	))
	type entry struct {
		time uint64
		pos  CueTrackPosition
	}
	var got []entry
	if !WalkCuePoints(cues, func(tm uint64, hasTime bool, pos CueTrackPosition) {
		if !hasTime {
			t.Fatal("point without time")
		}
		got = append(got, entry{tm, pos})
	}) {
		t.Fatal("well-formed cues reported incomplete")
	}
	want := []entry{
		{100, CueTrackPosition{Track: 1, ClusterPosition: 4000, RelativePosition: -1, Duration: -1}},
		{100, CueTrackPosition{Track: 3, ClusterPosition: 4000, RelativePosition: 77, Duration: 1500}},
		{2500, CueTrackPosition{Track: 3, ClusterPosition: 9000, RelativePosition: -1, Duration: -1, HasReference: true}},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	truncated := cues[:len(cues)-3]
	if WalkCuePoints(truncated, func(uint64, bool, CueTrackPosition) {}) {
		t.Fatal("truncated cues reported complete")
	}
}

func TestSeekEntries(t *testing.T) {
	seek := func(id, pos uint64) []byte {
		var idBytes []byte
		return AppendElement(nil, IDSeek, slices.Concat(
			AppendElement(nil, IDSeekID, AppendID(idBytes, id)), AppendUint(nil, IDSeekPosition, pos)))
	}
	head := slices.Concat(seek(IDInfo, 100), seek(IDCues, 1<<40), seek(IDSeekHead, 5000),
		AppendElement(nil, IDSeek, AppendUint(nil, IDSeekPosition, 9))) // no ID
	want := []SeekEntry{{IDInfo, 100}, {IDCues, 1 << 40}, {IDSeekHead, 5000}}
	var got []SeekEntry
	WalkSeekEntries(head, func(e SeekEntry) { got = append(got, e) })
	if !slices.Equal(got, want) {
		t.Fatalf("entries = %+v, want %+v", got, want)
	}
	if pos, ok := SeekPosition(head, IDCues); !ok || pos != 1<<40 {
		t.Fatalf("SeekPosition(Cues) = %d, %v", pos, ok)
	}
	if _, ok := SeekPosition(head, IDTags); ok {
		t.Fatal("found an absent entry")
	}
}

func TestParseTrackEntry(t *testing.T) {
	enc := AppendElement(nil, IDContentEncoding, slices.Concat(
		AppendUint(nil, IDContentEncodingScope, 3),
		AppendElement(nil, IDContentCompression, slices.Concat(
			AppendUint(nil, IDContentCompAlgo, CompressionHeaderStripping),
			AppendElement(nil, IDContentCompSettings, []byte{0x16, 0x00})))))
	entry := slices.Concat(
		AppendUint(nil, IDTrackNumber, 4), AppendUint(nil, IDTrackUID, 0xABCDEF),
		AppendUint(nil, IDTrackType, TrackTypeSubtitle), AppendString(nil, IDCodecID, "S_HDMV/PGS"),
		AppendUint(nil, IDFlagForced, 1), AppendUint(nil, IDFlagHearingImpaired, 1),
		AppendString(nil, IDLanguageBCP47, "pt-BR"), AppendString(nil, IDName, "Forced\x00\x00"),
		AppendElement(nil, IDContentEncodings, enc),
	)
	got := ParseTrackEntry(entry)
	if got.Number != 4 || got.UID != 0xABCDEF || got.Type != TrackTypeSubtitle || got.CodecID != "S_HDMV/PGS" {
		t.Fatalf("identity: %+v", got)
	}
	// Language defaults to eng and FlagDefault to set, as the spec says.
	if got.Language != "eng" || got.LanguageBCP47 != "pt-BR" || got.Name != "Forced" ||
		!got.Default || !got.Enabled || !got.Forced || !got.HearingImpaired || got.TimestampScale != 1 {
		t.Fatalf("flags and names: %+v", got)
	}
	want := []ContentEncoding{{Scope: 3, CompAlgo: CompressionHeaderStripping, CompSettings: []byte{0x16, 0x00}}}
	if len(got.Encodings) != 1 || got.Encodings[0].Scope != want[0].Scope || got.Encodings[0].CompAlgo != want[0].CompAlgo ||
		!bytes.Equal(got.Encodings[0].CompSettings, want[0].CompSettings) {
		t.Fatalf("encodings = %+v", got.Encodings)
	}
	// A crafted entry of many empty encodings keeps only the first few.
	empty := bytes.Repeat(AppendElement(nil, IDContentEncoding, nil), 10_000)
	got = ParseTrackEntry(AppendElement(nil, IDContentEncodings, empty))
	if len(got.Encodings) != MaxContentEncodings {
		t.Fatalf("kept %d of 10000 encodings", len(got.Encodings))
	}
}
