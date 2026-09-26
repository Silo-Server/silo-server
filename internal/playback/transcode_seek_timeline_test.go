package playback

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodedSeekSegmentsMatchSyntheticTimeline(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	encoders, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil || !strings.Contains(string(encoders), "libx264") {
		t.Skip("ffmpeg does not provide libx264")
	}
	source := filepath.Join(t.TempDir(), "source.mp4")
	if output, err := exec.CommandContext(ctx, ffmpeg,
		"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=128x72:rate=25",
		"-t", "66", "-c:v", "libx264", "-preset", "ultrafast", source,
	).CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic source: %v\n%s", err, output)
	}
	for _, seek := range []int{0, 26, 56} {
		t.Run(fmt.Sprintf("seek_%d", seek), func(t *testing.T) {
			dir := t.TempDir()
			args := buildFFmpegArgs(TranscodeOpts{
				InputPath: source, OutputDir: dir, FFmpegPath: ffmpeg,
				HWAccel: HWAccelNone, SourceVideoCodec: "h264",
				TargetCodecVideo: "h264", TargetCodecAudio: "copy",
				SeekSeconds: float64(seek), StartSegmentNumber: seek / 2,
				SegmentDuration: 2,
			})
			// Limit by frame count: with copyts, an output time limit would
			// include the source seek offset rather than just this generation.
			args = append(args[:len(args)-1], "-frames:v", "200", args[len(args)-1])
			if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
				t.Fatalf("encode seek generation: %v\n%s", err, output)
			}
			manifest, err := os.ReadFile(filepath.Join(dir, "stream.m3u8"))
			if err != nil {
				t.Fatal(err)
			}
			timeline, err := parseManifestTimeline(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if len(timeline.entries) != 4 {
				t.Fatalf("eight seconds must produce four synthetic two-second segments:\n%s", manifest)
			}
			for i, entry := range timeline.entries {
				if entry.number != seek/2+i || math.Abs(entry.duration-2) > 0.001 {
					t.Fatalf("segment %d disagrees with synthetic timeline:\n%s", i, manifest)
				}
			}
		})
	}
}
