package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Fansub releases can attach more font data than the bundle limit. The bundle
// must skip a font that does not fit and fill its place with later fonts,
// rather than fail and leave none.
func TestExtractAttachedSubtitleFontsSkipsFontsOverByteLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	streams := []string{fmt.Sprintf(`{"index":2,"extradata_size":%d,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"huge.ttf"}}`, maxSubtitleFontBytes+1)}
	want := make([]string, 0, maxSubtitleFontAttachments)
	for i := 3; i < 3+maxSubtitleFontAttachments; i++ {
		streams = append(streams, fmt.Sprintf(`{"index":%d,"extradata_size":1,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"f%d.ttf"}}`, i, i))
		want = append(want, fmt.Sprintf("f%d.ttf:1", i))
	}
	writeExecutable(t, filepath.Join(dir, "ffprobe"), "#!/bin/sh\nprintf '%s' '{\"streams\":["+strings.Join(streams, ",")+"]}'\n")
	writeExecutable(t, ffmpeg, `#!/bin/sh
for arg do
 case "$arg" in
  -dump_attachment:2) exit 42 ;;
  -dump_attachment:*) printf f ;;
 esac
done
`)
	fonts, err := ExtractAttachedSubtitleFonts(t.Context(), "input.mkv", ffmpeg)
	if err != nil {
		t.Fatalf("ExtractAttachedSubtitleFonts returned error: %v", err)
	}
	got := make([]string, 0, len(fonts))
	for _, font := range fonts {
		got = append(got, fmt.Sprintf("%s:%d", font.Name, len(font.Data)))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fonts = %v, want %v", got, want)
	}
}

// Missing bytes cannot be assigned to the next font in a concatenated bundle.
func TestDumpFontAttachmentsRejectsMissingData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	// Only emits the first attachment, making the probed framing inconsistent.
	writeExecutable(t, ffmpegPath, `#!/bin/sh
prev=""
first=1
for a in "$@"; do
  case "$prev" in
    -dump_attachment:*)
      if [ "$first" = "1" ]; then printf 'fontdata'; first=0; fi ;;
  esac
  prev="$a"
done
`)

	fonts, err := dumpFontAttachments(context.Background(), "input.mkv", ffmpegPath,
		[]attachmentProbeStream{{Index: 2, ExtraDataSize: 8}, {Index: 3, ExtraDataSize: 8}}, 0, maxSubtitleFontBytes)
	if err == nil || len(fonts) != 0 {
		t.Fatalf("missing attachment data accepted: %d fonts, %v", len(fonts), err)
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
printf '%s' '{"streams":[{"index":1,"extradata_size":1,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"font.ttf"}}]}'
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
