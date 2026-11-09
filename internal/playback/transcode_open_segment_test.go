package playback

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSegmentServesFilesInsideOutputDir(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		hlsInitSegmentName: "init",
		"seg_00001.m4s":    "media",
		"sub_00001.vtt":    "WEBVTT",
	} {
		if err := os.WriteFile(filepath.Join(outputDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	session := NewTranscodeSessionForTest(outputDir)

	for name, want := range map[string]string{
		hlsInitSegmentName: "init",
		"seg_00001.m4s":    "media",
		"sub_00001.vtt":    "WEBVTT",
	} {
		lease, err := session.OpenSegment(name)
		if err != nil {
			t.Fatalf("OpenSegment(%q): %v", name, err)
		}
		// The descriptor must outlive the os.Root it was opened through.
		got, readErr := io.ReadAll(lease.File)
		_ = lease.Close()
		if readErr != nil || string(got) != want {
			t.Fatalf("OpenSegment(%q) body = %q, %v; want %q", name, got, readErr, want)
		}
	}
}

func TestOpenSegmentRefusesNamesOutsideOutputDir(t *testing.T) {
	base := t.TempDir()
	outputDir := filepath.Join(base, "session")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "movie.mkv")
	if err := os.WriteFile(source, []byte("source media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "seg_00001.m4s"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	// prepareSubtitleFilterInput creates this alias for burned-in text
	// subtitles; it points at the library file, not at a segment.
	opts := TranscodeOpts{
		InputPath:          source,
		OutputDir:          outputDir,
		SubtitleBurnIn:     true,
		SubtitleTrackIndex: 0,
		SubtitleCodec:      "subrip",
	}
	if err := prepareSubtitleFilterInput(t.Context(), &opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(outputDir, subtitleFilterAliasName)); err != nil {
		t.Fatalf("subtitle alias not created: %v", err)
	}
	// Another escaping symlink under an ordinary segment name.
	if err := os.Symlink(source, filepath.Join(outputDir, "seg_00002.m4s")); err != nil {
		t.Fatal(err)
	}
	session := NewTranscodeSessionForTest(outputDir)

	for _, name := range []string{
		"..",
		".",
		"",
		"../seg_00001.m4s",
		"sub/seg_00001.m4s",
		source,
		subtitleFilterAliasName,
		"seg_00002.m4s",
	} {
		lease, err := session.OpenSegment(name)
		if err == nil {
			_ = lease.Close()
			t.Fatalf("OpenSegment(%q) succeeded, want refusal", name)
		}
		if name != "seg_00002.m4s" && !errors.Is(err, ErrSegmentNotFound) {
			t.Fatalf("OpenSegment(%q) error = %v, want ErrSegmentNotFound", name, err)
		}
	}
}

func TestOpenSegmentDV81(t *testing.T) {
	box := func(typ string, parts ...[]byte) []byte {
		body := bytes.Join(parts, nil)
		return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), append([]byte(typ), body...)...)
	}
	nal := func(payload []byte) []byte {
		return append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload...)
	}
	hvcC := make([]byte, 23)
	hvcC[21] = 3 // 4-byte NAL lengths
	initMP4 := box("moov", box("trak",
		box("tkhd", make([]byte, 12), []byte{0, 0, 0, 1}, make([]byte, 64)),
		box("mdia",
			box("hdlr", make([]byte, 8), []byte("vide"), make([]byte, 13)),
			box("minf", box("stbl", box("stsd", []byte{0, 0, 0, 0, 0, 0, 0, 1},
				box("hvc1", make([]byte, 78), box("hvcC", hvcC), box("dvcC", []byte{1, 0, 0x0e, 0x37, 0x60}))))))))
	mel, err := os.ReadFile(filepath.Join("dovirpu", "testdata", "mel_orig.bin")) // a start code, then the RPU
	if err != nil {
		t.Fatal(err)
	}
	segment := func(rpu []byte) []byte {
		sample := append(nal([]byte{0x02, 0x01, 0xAA}), nal(append([]byte{0x7C, 0x01}, rpu...))...)
		trun := binary.BigEndian.AppendUint32([]byte{0, 0, 0x02, 0x01, 0, 0, 0, 1, 0, 0, 0, 0}, uint32(len(sample)))
		moof := box("moof", box("traf", box("tfhd", []byte{0, 0x02, 0, 0, 0, 0, 0, 1}), box("trun", trun)))
		binary.BigEndian.PutUint32(moof[len(moof)-8:], uint32(len(moof)+8))
		return append(moof, box("mdat", sample)...)
	}

	outputDir := t.TempDir()
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(outputDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(hlsInitSegmentName, initMP4)
	write("seg_00001.m4s", segment([]byte{0x19, 0x08, 0x09, 0, 0, 0, 0, 0x80})) // CRC mismatch: cannot convert
	write("seg_00002.m4s", segment(mel[4:]))
	session := NewTranscodeSessionForTest(outputDir)
	session.opts.VideoBitstreamFilter = DV7ToDV81BitstreamFilter

	lease, err := session.OpenSegment(hlsInitSegmentName)
	if err != nil {
		t.Fatalf("init.mp4: %v", err)
	}
	_ = lease.Close()
	// A segment none of whose RPUs converted must not be served under the
	// Dolby Vision 8.1 claim, on a retry either.
	for attempt := 1; attempt <= 2; attempt++ {
		if lease, err := session.OpenSegment("seg_00001.m4s"); err == nil || errors.Is(err, ErrSegmentNotFound) {
			if lease != nil {
				_ = lease.Close()
			}
			t.Fatalf("unconvertible segment, attempt %d: err = %v, want a conversion failure", attempt, err)
		}
	}
	// FFmpeg recreates init.mp4 when it restarts. A media segment must not
	// depend on it once its track is known.
	write(hlsInitSegmentName, nil)
	lease, err = session.OpenSegment("seg_00002.m4s")
	if err != nil {
		t.Fatalf("segment while init.mp4 is rewritten: %v", err)
	}
	_ = lease.Close()
}
