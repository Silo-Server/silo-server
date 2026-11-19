package playback

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStreamExtractSubtitleBoundsWindow(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to verify subtitle window timestamps")
	}
	source := filepath.Join(t.TempDir(), "captions.srt")
	const input = "1\n00:00:00,000 --> 00:00:05,000\nBefore window\n\n2\n00:13:20,000 --> 00:13:25,000\nInside window\n\n3\n00:16:40,000 --> 00:16:45,000\nAfter window\n"
	if err := os.WriteFile(source, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := StreamExtractSubtitle(t.Context(), StreamExtractOpts{
		InputPath: source, SourceCodec: "subrip", SeekSeconds: 800, DurationSeconds: 100,
		FFmpegPath: bin, Writer: &output,
	}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Inside window") ||
		!strings.Contains(got, "13:20.000 --> 13:25.000") ||
		strings.Contains(got, "Before window") || strings.Contains(got, "After window") {
		t.Fatalf("expected only the in-window cue at its source timestamp, got %q", got)
	}
}

type failingSubtitleWriter struct{ err error }

func (w failingSubtitleWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStreamExtractSubtitleStopsOnWriterFailure(t *testing.T) {
	// Keep the extractor producing output after the response writer fails.
	// Waiting for it without canceling first leaves it blocked on a full pipe.
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec cat /dev/zero\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	writeErr := errors.New("response writer disconnected")
	err := StreamExtractSubtitle(ctx, StreamExtractOpts{
		InputPath: "unused.mkv", FFmpegPath: bin, Writer: failingSubtitleWriter{writeErr},
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("extract error = %v, want writer error %v", err, writeErr)
	}
	if ctx.Err() != nil {
		t.Fatalf("extract required request timeout to stop: %v", ctx.Err())
	}
}
