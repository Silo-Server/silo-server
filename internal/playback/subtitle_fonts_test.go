package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeFFmpegDumping returns a shell script that mimics ffmpeg's
// -dump_attachment behaviour: it writes payload to every path that follows a
// -dump_attachment:* flag. This exercises the single-invocation extractor
// without a real ffmpeg. The shebang must stay on the first line.
func fakeFFmpegDumping(payload string) string {
	return "#!/bin/sh\nPAYLOAD='" + payload + `'
prev=""
for a in "$@"; do
  case "$prev" in
    -dump_attachment:*) printf '%s' "$PAYLOAD" > "$a" ;;
  esac
  prev="$a"
done
`
}

// TestDumpFontAttachmentsArgvOrder pins the ffmpeg argument contract: every
// -dump_attachment flag must precede -i (they are per-input options for the
// following input), and the -map 0:t? / -c copy stream-copy flags must be
// present (without them ffmpeg decodes the whole video). A fake ffmpeg records
// its argv so a malformed invocation can't pass silently.
func TestDumpFontAttachmentsArgvOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpegPath, "#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+argvFile+`'
prev=""
for a in "$@"; do
  case "$prev" in
    -dump_attachment:*) printf 'x' > "$a" ;;
  esac
  prev="$a"
done
`)

	if _, err := dumpFontAttachments(context.Background(), "input.mkv", ffmpegPath,
		[]attachmentProbeStream{{Index: 2}, {Index: 5}}, maxSubtitleFontBytes); err != nil {
		t.Fatalf("dumpFontAttachments returned error: %v", err)
	}

	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	argv := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	inputIdx, mapIdx := -1, -1
	lastDumpIdx := -1
	for i, a := range argv {
		switch {
		case a == "-i":
			inputIdx = i
		case a == "-map":
			mapIdx = i
		case strings.HasPrefix(a, "-dump_attachment:"):
			lastDumpIdx = i
		}
	}
	if inputIdx < 0 {
		t.Fatalf("argv missing -i: %v", argv)
	}
	if lastDumpIdx < 0 || lastDumpIdx > inputIdx {
		t.Fatalf("dump_attachment flags must precede -i; argv=%v", argv)
	}
	if mapIdx < 0 || argv[mapIdx+1] != "0:t?" {
		t.Fatalf("argv missing -map 0:t?: %v", argv)
	}
	if !containsSeq(argv, "-c", "copy") {
		t.Fatalf("argv missing -c copy: %v", argv)
	}
}

func containsSeq(argv []string, a, b string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == a && argv[i+1] == b {
			return true
		}
	}
	return false
}

func TestExtractAttachedSubtitleFontsSingleInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, filepath.Join(dir, "ffprobe"), `#!/bin/sh
cat <<'JSON'
{"streams":[{"index":2,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"MyFont.ttf","mimetype":"font/ttf"}},{"index":3,"codec_name":"otf","codec_type":"attachment","tags":{"filename":"Other.otf","mimetype":"font/otf"}}]}
JSON
`)
	writeExecutable(t, ffmpegPath, fakeFFmpegDumping("fontdata"))

	fonts, err := ExtractAttachedSubtitleFonts(context.Background(), "input.mkv", ffmpegPath)
	if err != nil {
		t.Fatalf("ExtractAttachedSubtitleFonts returned error: %v", err)
	}
	if len(fonts) != 2 {
		t.Fatalf("font count = %d, want 2", len(fonts))
	}
	if fonts[0].Name != "MyFont.ttf" || fonts[1].Name != "Other.otf" {
		t.Fatalf("font names = %q/%q, want MyFont.ttf/Other.otf", fonts[0].Name, fonts[1].Name)
	}
	if string(fonts[0].Data) != "fontdata" {
		t.Fatalf("font data = %q, want fontdata", string(fonts[0].Data))
	}
}

func TestExtractAttachedSubtitleFontDumpsOnlyRequestedStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, filepath.Join(dir, "ffprobe"), `#!/bin/sh
printf '%s' '{"streams":[{"index":2,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"first.ttf"}},{"index":7,"codec_name":"otf","codec_type":"attachment"}]}'
`)
	writeExecutable(t, ffmpeg, `#!/bin/sh
for arg do
 case "$arg" in -dump_attachment:7) ;; -dump_attachment:*) exit 42;; esac
done
`+strings.TrimPrefix(fakeFFmpegDumping("selected-font"), "#!/bin/sh\n"))
	font, err := ExtractAttachedSubtitleFont(t.Context(), "input.mkv", ffmpeg, 7)
	if err != nil || font == nil || font.StreamIndex != 7 || font.Name != "attachment-1.otf" || string(font.Data) != "selected-font" {
		t.Fatalf("selected font=%+v err=%v", font, err)
	}
	metadata, err := ListAttachedSubtitleFonts(t.Context(), "input.mkv", ffmpeg)
	if err != nil || len(metadata) != 2 || metadata[1].FileName != font.Name {
		t.Fatalf("selected fallback name differs from discovery: %+v %v", metadata, err)
	}
	writeExecutable(t, ffmpeg, "#!/bin/sh\nexit 42\n")
	font, err = ExtractAttachedSubtitleFont(t.Context(), "input.mkv", ffmpeg, 99)
	if err != nil || font != nil {
		t.Fatalf("unknown stream started extraction: %+v %v", font, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ExtractAttachedSubtitleFont(ctx, "input.mkv", ffmpeg, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled single-font extraction: %v", err)
	}
}

// A dump file ffmpeg never wrote (an attachment it could not stream-copy) must
// be skipped rather than fail the whole bundle.
func TestDumpFontAttachmentsSkipsMissingDumps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	// Only writes the first dump target; the second path is left absent.
	writeExecutable(t, ffmpegPath, `#!/bin/sh
prev=""
first=1
for a in "$@"; do
  case "$prev" in
    -dump_attachment:*)
      if [ "$first" = "1" ]; then printf 'fontdata' > "$a"; first=0; fi ;;
  esac
  prev="$a"
done
`)

	fonts, err := dumpFontAttachments(context.Background(), "input.mkv", ffmpegPath,
		[]attachmentProbeStream{{Index: 2}, {Index: 3}}, maxSubtitleFontBytes)
	if err != nil {
		t.Fatalf("dumpFontAttachments returned error: %v", err)
	}
	if len(fonts) != 1 {
		t.Fatalf("font count = %d, want 1 (missing dump skipped)", len(fonts))
	}
}

func TestDumpFontAttachmentsRejectsOverLimitData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpegPath, fakeFFmpegDumping("12345"))

	_, err := dumpFontAttachments(
		context.Background(),
		"input.mkv",
		ffmpegPath,
		[]attachmentProbeStream{{Index: 2}},
		4,
	)
	if err == nil {
		t.Fatal("expected size limit error, got nil")
	}
	if !strings.Contains(err.Error(), "attached font data exceeds") {
		t.Fatalf("error = %q, want attached font data limit", err.Error())
	}
}

func TestFFprobePathFromFFmpegRewritesOnlyBasename(t *testing.T) {
	got := ffprobePathFromFFmpeg(filepath.Join("tmp", "ffmpeg-tools", "ffmpeg"))
	want := filepath.Join("tmp", "ffmpeg-tools", "ffprobe")
	if got != want {
		t.Fatalf("ffprobePathFromFFmpeg basename path = %q, want %q", got, want)
	}

	got = ffprobePathFromFFmpeg(filepath.Join("tmp", "ffmpeg-tools", "custom"))
	if got != "ffprobe" {
		t.Fatalf("ffprobePathFromFFmpeg custom basename = %q, want ffprobe", got)
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFontExtractionHonorsDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	ffprobe := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(ffprobe, []byte(`#!/bin/sh
printf '%s' '{"streams":[{"index":1,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"font.ttf"}}]}'
`), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := ExtractAttachedSubtitleFonts(ctx, "fixture.mkv", ffmpeg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("extraction did not stop at deadline")
	}
}
