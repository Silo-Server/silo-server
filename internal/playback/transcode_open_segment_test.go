package playback

import (
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
	if err := prepareSubtitleFilterInput(&opts); err != nil {
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
