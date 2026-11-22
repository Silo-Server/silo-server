package mediasample

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realFFmpeg returns an ffmpeg from PATH and its capabilities, or skips.
func realFFmpeg(t *testing.T) (string, Capabilities) {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	caps, err := LoadCapabilities(ctx, path)
	if err != nil {
		t.Skipf("ffmpeg capabilities unavailable: %v", err)
	}
	return path, caps
}

// TestRunSheetsRejectsPrematureEOFWithRealFFmpeg requests an eight-second
// timeline from a two-second MPEG-TS source. A complete source's final
// eight-second GOP must still provide every preview, including when the
// sample window and the container timestamps start after zero.
func TestRunSheetsRejectsPrematureEOFWithRealFFmpeg(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	for _, shift := range []int{0, 10} {
		for _, offset := range []float64{0, 11.4} {
			if shift > 0 && offset > 0 {
				continue // each start after zero is covered on its own
			}
			for _, duration := range []int{2, 8} {
				t.Run(fmt.Sprintf("sample-shift-%d/container-offset-%g/duration-%d", shift, offset, shift+duration), func(t *testing.T) {
					clip := filepath.Join(t.TempDir(), "clip.ts")
					args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
						fmt.Sprintf("color=c=gray:s=64x36:r=24:d=%d", shift+duration), "-c:v", "libx264",
						"-threads", "1", "-g", "240", "-keyint_min", "240", "-sc_threshold", "0", "-bf", "2",
						"-output_ts_offset", formatSeconds(offset), clip}
					if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
						t.Skipf("cannot generate the clip: %v: %s", err, output)
					}
					if offset > 0 {
						output, err := exec.Command(ffmpeg, probeArgs(clip)...).CombinedOutput()
						if err != nil {
							t.Fatalf("probe shifted clip: %v: %s", err, output)
						}
						header := &inputHeaderParser{}
						for line := range strings.SplitSeq(string(output), "\n") {
							header.line(line)
						}
						if header.info.StartSeconds < offset {
							t.Fatalf("container starts at %g, want at least %g", header.info.StartSeconds, offset)
						}
					}
					for _, readThrough := range []bool{false, true} {
						t.Run(fmt.Sprintf("read-through-%t", readThrough), func(t *testing.T) {
							req := Request{Input: clip, Samples: &Samples{Seconds: secondsFrom(float64(shift+1), 2, 4), ReadThrough: readThrough},
								Sheets: &SheetsOutput{TileWidth: 24, TileHeight: 16, Columns: 2, Rows: 2, Quality: 80}, Threads: 1}
							ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
							defer cancel()
							result, err := (Runner{FFmpegPath: ffmpeg}).Run(ctx, req)
							if duration == 2 {
								failure, ok := errors.AsType[*Error](err)
								if !ok || failure.Reason != ReasonEmpty {
									t.Fatalf("frames %+v error %v, want sparse output rejected", result.SheetFrames, err)
								}
							} else if err != nil || result.SheetFrames != (SheetFrames{Decoded: 4}) {
								t.Fatalf("valid final GOP: frames %+v error %v", result.SheetFrames, err)
							}
						})
					}
				})
			}
		}
	}
}
