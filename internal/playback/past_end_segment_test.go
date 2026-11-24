package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The past-end TS segment is a PAT and PMT a demuxer accepts: the generic PAT
// is byte for byte the one FFmpeg writes, CRC included, and the tables of a
// session's own segment are found behind the SDT FFmpeg writes first.
func TestPastEndTSProgramTables(t *testing.T) {
	ffmpegPAT := []byte{0x47, 0x40, 0x00, 0x10, 0x00, 0x00, 0xb0, 0x0d, 0x00, 0x01, 0xc1, 0x00, 0x00, 0x00, 0x01, 0xf0, 0x00, 0x2a, 0xb1, 0x04, 0xb2}
	generic := genericTSProgramTables()
	if len(generic) != 2*tsPacketSize || !bytes.HasPrefix(generic, ffmpegPAT) {
		t.Fatalf("generic PAT = % x, want FFmpeg's % x", generic[:len(ffmpegPAT)], ffmpegPAT)
	}

	sdt := tsSectionPacket(0x11, []byte{0x42, 0xf0, 0x00, 0x00, 0x01, 0xc1, 0x00, 0x00, 0xff, 0x01, 0xff})
	media := bytes.Repeat([]byte{0x47, 0x01, 0x00, 0x10}, tsPacketSize/4)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seg_00074.ts"), bytes.Join([][]byte{sdt, generic, media}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if got := readTSProgramTables(root, "seg_00074.ts"); !bytes.Equal(got, generic) {
		t.Fatalf("readTSProgramTables() = % x, want the PAT and PMT", got)
	}

	// A PAT packet whose lengths overrun it names no PMT rather than panic.
	for name, mutate := range map[string]func(p []byte){
		"adaptation field": func(p []byte) { p[3], p[4] = 0x30, 0xff },
		"pointer field":    func(p []byte) { p[4] = 0xff },
		"section length":   func(p []byte) { p[6], p[7] = 0xb0, 0x05 },
	} {
		packet := append([]byte(nil), generic[:tsPacketSize]...)
		mutate(packet)
		if got := patProgramMapPID(packet); got != -1 {
			t.Errorf("%s: patProgramMapPID() = %d, want -1", name, got)
		}
	}
}

// The past-end fMP4 fragment gives each track of the init segment an empty
// run decoding from the segment's start in that track's timescale.
func TestPastEndFMP4Fragment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, hlsInitSegmentName), testInit(), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &TranscodeSession{outputDir: dir, opts: TranscodeOpts{SegmentDuration: 2}}
	top, err := fmp4Boxes(s.emptyFMP4Fragment(75))
	if err != nil || len(top) != 2 || top[0].typ != "moof" || top[1].typ != "mdat" || len(top[1].payload) != 0 {
		t.Fatalf("fragment boxes = %+v, %v; want a moof and an empty mdat", top, err)
	}
	trafs, err := fmp4Boxes(top[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32]uint64{1: 150 * 12800, 2: 150 * 48000}
	for _, traf := range trafs[1:] {
		tfhd, _ := fmp4Child(traf.payload, "tfhd")
		tfdt, _ := fmp4Child(traf.payload, "tfdt")
		trun, _ := fmp4Child(traf.payload, "trun")
		id := binary.BigEndian.Uint32(tfhd[4:])
		if got := binary.BigEndian.Uint64(tfdt[4:]); got != want[id] || binary.BigEndian.Uint32(trun[4:]) != 0 {
			t.Fatalf("track %d decodes from %d with %d samples, want %d with none", id, got, binary.BigEndian.Uint32(trun[4:]), want[id])
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("tracks %v missing from the fragment", want)
	}
}

type streamEndStoreFunc func(ctx context.Context, fileID, from, seconds int) (bool, error)

func (f streamEndStoreFunc) ShortenDuration(ctx context.Context, fileID, from, seconds int, _ time.Time) (bool, error) {
	return f(ctx, fileID, from, seconds)
}

// The recorder stores a file's end once, rounded up so the partial last
// second keeps its segment, and only from the duration the transcode was
// planned with; an end at or past that duration, or that rounds up to it,
// writes nothing.
func TestStreamEndRecorder(t *testing.T) {
	calls := make(chan int, 4)
	r := NewStreamEndRecorder(streamEndStoreFunc(func(_ context.Context, _, from, seconds int) (bool, error) {
		if from != 160 {
			t.Errorf("write from %d s, want the planned 160", from)
		}
		calls <- seconds
		return true, nil
	}))
	next := func() int {
		select {
		case seconds := <-calls:
			return seconds
		case <-time.After(5 * time.Second):
			t.Fatal("no write")
			return 0
		}
	}
	readSince := time.Now()
	note := func(planned, end float64) {
		r.Note(context.Background(), 7, StreamEnd{Seconds: end, PlannedSeconds: planned, ReadSince: readSince})
	}
	note(160, 149.5)
	if got := next(); got != 150 {
		t.Fatalf("stored %d s, want 150", got)
	}
	note(160, 149.5)
	note(149, 149.5)
	note(150, 149.4) // rounds up to the planned duration
	note(160, 140.2)
	if got := next(); got != 141 {
		t.Fatalf("stored %d s, want the repeated end skipped and 141 next", got)
	}
}
