package playback

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtractAttachedSubtitleFontsUsesBoundedStdoutExtraction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, filepath.Join(dir, "ffprobe"), `#!/bin/sh
cat <<'JSON'
{"streams":[{"index":2,"codec_name":"ttf","codec_type":"attachment","tags":{"filename":"MyFont.ttf","mimetype":"font/ttf"}}]}
JSON
`)
	writeExecutable(t, ffmpegPath, `#!/bin/sh
printf 'fontdata'
`)

	fonts, err := ExtractAttachedSubtitleFonts(context.Background(), "input.mkv", ffmpegPath)
	if err != nil {
		t.Fatalf("ExtractAttachedSubtitleFonts returned error: %v", err)
	}
	if len(fonts) != 1 {
		t.Fatalf("font count = %d, want 1", len(fonts))
	}
	if fonts[0].Name != "MyFont.ttf" {
		t.Fatalf("font name = %q, want MyFont.ttf", fonts[0].Name)
	}
	if string(fonts[0].Data) != "fontdata" {
		t.Fatalf("font data = %q, want fontdata", string(fonts[0].Data))
	}
}

func TestExtractFontAttachmentRejectsOverLimitData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test helper is unix-only")
	}

	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpegPath, `#!/bin/sh
printf '12345'
`)

	_, err := extractFontAttachment(
		context.Background(),
		"input.mkv",
		ffmpegPath,
		attachmentProbeStream{Index: 2},
		"font.ttf",
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
