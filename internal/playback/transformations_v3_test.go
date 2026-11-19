package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeTransformationRegistryWithToneMapV3ResultPreservesDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := ProbeTransformationRegistryWithToneMapV3Result(ctx, "ffmpeg", nil)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe error = %v, want context deadline", err)
	}
}

// writeProbeFFmpegV3 writes an executable fake FFmpeg script for the
// transformation registry probes and returns its path.
func writeProbeFFmpegV3(t *testing.T, script string) string {
	t.Helper()
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ffmpeg
}
