package mediaprobe

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"testing"
)

// ebmlElement encodes one EBML element with an 8-byte data size, which keeps
// offsets easy to compute in the seek-head test.
func ebmlElement(id uint64, payload ...[]byte) []byte {
	var out []byte
	for shift := 24; shift >= 0; shift -= 8 {
		if b := byte(id >> shift); b != 0 || len(out) > 0 {
			out = append(out, b)
		}
	}
	data := bytes.Join(payload, nil)
	size := uint64(len(data))
	out = append(out, 0x01)
	for shift := 48; shift >= 0; shift -= 8 {
		out = append(out, byte(size>>shift))
	}
	return append(out, data...)
}

func ebmlUintElement(id, v uint64) []byte {
	return ebmlElement(id, []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func trackEntry(number, kind uint64, codec string) []byte {
	return ebmlElement(mkvIDTrackEntry,
		ebmlUintElement(mkvIDTrackNumber, number),
		ebmlUintElement(mkvIDTrackType, kind),
		ebmlElement(mkvIDCodecID, []byte(codec)),
	)
}

func ebmlHeader(docType string) []byte {
	return ebmlElement(ebmlIDHeader, ebmlElement(ebmlIDDocType, []byte(docType)))
}

var testTracks = []MatroskaTrack{
	{Number: 1, Type: MatroskaTrackTypeVideo, CodecID: "V_MPEG4/ISO/AVC"},
	{Number: 4, Type: MatroskaTrackTypeAudio, CodecID: "A_EAC3"},
	{Number: 5, Type: MatroskaTrackTypeSubtitle, CodecID: "S_TEXT/UTF8"},
}

func testTracksElement() []byte {
	var entries [][]byte
	for _, track := range testTracks {
		entries = append(entries, trackEntry(track.Number, track.Type, track.CodecID))
	}
	return ebmlElement(mkvIDTracks, entries...)
}

func readTracks(t *testing.T, file []byte) ([]MatroskaTrack, error) {
	t.Helper()
	return ReadMatroskaTracks(bytes.NewReader(file), int64(len(file)))
}

func TestReadMatroskaTracksBeforeClusters(t *testing.T) {
	for _, docType := range []string{"matroska", "webm"} {
		file := append(ebmlHeader(docType), ebmlElement(mkvIDSegment,
			ebmlElement(0xEC, make([]byte, 32)), // Void
			testTracksElement(),
			ebmlElement(mkvIDCluster, []byte{0xE7, 0x81, 0x00}),
		)...)
		tracks, err := readTracks(t, file)
		if err != nil {
			t.Fatalf("%s: %v", docType, err)
		}
		if !reflect.DeepEqual(tracks, testTracks) {
			t.Fatalf("%s: tracks = %+v, want %+v", docType, tracks, testTracks)
		}
	}
}

func TestReadMatroskaTracksThroughSeekHeadAfterClusters(t *testing.T) {
	cluster := ebmlElement(mkvIDCluster, make([]byte, 64))
	seekHeadFor := func(tracksPos uint64) []byte {
		return ebmlElement(mkvIDSeekHead, ebmlElement(mkvIDSeek,
			ebmlElement(mkvIDSeekID, []byte{0x16, 0x54, 0xAE, 0x6B}),
			ebmlUintElement(mkvIDSeekPosition, tracksPos),
		))
	}
	// Tracks follows the seek head and the cluster; positions are relative
	// to the segment's data.
	tracksPos := uint64(len(seekHeadFor(0)) + len(cluster))
	file := append(ebmlHeader("matroska"), ebmlElement(mkvIDSegment, seekHeadFor(tracksPos), cluster, testTracksElement())...)
	tracks, err := readTracks(t, file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tracks, testTracks) {
		t.Fatalf("tracks = %+v, want %+v", tracks, testTracks)
	}
}

func TestReadMatroskaTracksRejectsOtherFiles(t *testing.T) {
	if _, err := readTracks(t, ebmlHeader("notmkv")); !errors.Is(err, ErrNotMatroska) {
		t.Fatalf("other doc type: err = %v", err)
	}
	if _, err := readTracks(t, []byte("ftypisom0000")); !errors.Is(err, ErrNotMatroska) {
		t.Fatalf("mp4: err = %v", err)
	}
	noTracks := append(ebmlHeader("matroska"), ebmlElement(mkvIDSegment, ebmlElement(mkvIDCluster, make([]byte, 8)))...)
	if _, err := readTracks(t, noTracks); !errors.Is(err, ErrMatroskaTracksNotFound) {
		t.Fatalf("no tracks: err = %v", err)
	}
}

func TestReadMatroskaTracksFromFFmpegFile(t *testing.T) {
	data, err := os.ReadFile("../scanner/testdata/subtitles.mkv")
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := readTracks(t, data)
	if err != nil {
		t.Fatal(err)
	}
	want := []MatroskaTrack{
		{Number: 1, Type: MatroskaTrackTypeVideo, CodecID: "V_MPEG4/ISO/AVC"},
		{Number: 2, Type: MatroskaTrackTypeAudio, CodecID: "A_OPUS"},
		{Number: 3, Type: MatroskaTrackTypeSubtitle, CodecID: "S_TEXT/UTF8"},
		{Number: 4, Type: MatroskaTrackTypeSubtitle, CodecID: "S_TEXT/ASS"},
	}
	if !reflect.DeepEqual(tracks, want) {
		t.Fatalf("tracks = %+v, want %+v", tracks, want)
	}
}

// FFprobe refuses more than max_streams TrackEntries, so the reader does too
// instead of decoding every entry a corrupt Tracks payload claims.
func TestReadMatroskaTracksRefusesMoreEntriesThanFFmpeg(t *testing.T) {
	for _, tc := range []struct {
		entries int
		wantErr bool
	}{
		{maxTrackEntries, false},
		{maxTrackEntries + 1, true},
	} {
		entries := make([][]byte, tc.entries)
		for i := range entries {
			entries[i] = trackEntry(uint64(i+1), MatroskaTrackTypeSubtitle, "S_TEXT/UTF8")
		}
		file := append(ebmlHeader("matroska"), ebmlElement(mkvIDSegment, ebmlElement(mkvIDTracks, entries...))...)
		tracks, err := readTracks(t, file)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%d entries: err = %v, wantErr %v", tc.entries, err, tc.wantErr)
		}
		if !tc.wantErr && len(tracks) != tc.entries {
			t.Fatalf("%d entries: read %d tracks", tc.entries, len(tracks))
		}
	}
}

type failingReaderAt struct{ err error }

func (r failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, r.err }

// A reader that fails is reported as its error, never as a file that is not
// Matroska, so a caller can retry storage instead of recording the file.
func TestReadMatroskaTracksReportsReaderErrors(t *testing.T) {
	readErr := errors.New("input/output error")
	_, err := ReadMatroskaTracks(failingReaderAt{err: readErr}, 1<<20)
	if !errors.Is(err, readErr) || errors.Is(err, ErrNotMatroska) {
		t.Fatalf("err = %v, want the reader's error and not ErrNotMatroska", err)
	}

	// Bytes that end early are the file's content, not a failed read.
	if _, err := ReadMatroskaTracks(bytes.NewReader([]byte{0x1A, 0x45}), 2); !errors.Is(err, ErrNotMatroska) {
		t.Fatalf("err = %v for a truncated header, want ErrNotMatroska", err)
	}
}
