package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Exercise the production argument builder with cheap, synthetic variable-GOP
// media. A source timestamp offset catches confusion between raw PTS and the
// source-relative currentTime exposed to clients.
func TestCompatCopySeekSourceTimelineRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe := ffprobePathFromFFmpeg(ffmpeg)
	if _, err := exec.LookPath(ffprobe); err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	run := func(binary string, args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(binary), err, out)
		}
		return out
	}
	encoders := run(ffmpeg, "-hide_banner", "-encoders")
	if !strings.Contains(string(encoders), "libx264") {
		t.Skip("libx264 is unavailable")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "variable-gop.mkv")
	run(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=2", "-t", "930",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "1000", "-bf", "2",
		"-sc_threshold", "0", "-force_key_frames", "0,11,29,101,223,401,607,803,887,907,919",
		"-an", source)
	shifted := filepath.Join(dir, "shifted.mkv")
	run(ffmpeg, "-v", "error", "-i", source, "-c", "copy", "-output_ts_offset", "7", shifted)
	for _, input := range []string{source, shifted} {
		for _, requested := range []float64{900, 23.5} {
			t.Run(fmt.Sprintf("%s/seek-%g", filepath.Base(input), requested), func(t *testing.T) {
				anchor, segment, err := ResolveCopySeekAnchor(ctx, ffmpeg, input, requested, 2)
				if err != nil {
					t.Fatal(err)
				}
				output := t.TempDir()
				opts := TranscodeOpts{InputPath: input, OutputDir: output, SessionID: "compat-seek-test",
					SourceVideoCodec: "h264", SeekSeconds: requested, StreamOriginSeconds: anchor,
					CopySeekAnchorResolved: true, TargetCodecVideo: "copy", TargetCodecAudio: "copy",
					SegmentDuration: 2, StartSegmentNumber: segment, FFmpegPath: ffmpeg}
				run(ffmpeg, buildFFmpegArgs(opts)...)
				manifest, err := os.ReadFile(filepath.Join(output, "stream.m3u8"))
				if err != nil {
					t.Fatal(err)
				}
				aligned, err := AlignRealManifestToSourceTimeline(manifest, opts, "gap.m4s")
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(manifest), fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", segment)) {
					t.Fatalf("FFmpeg media sequence does not match anchor segment %d: %s", segment, manifest)
				}
				if !strings.Contains(string(aligned), "#EXT-X-MEDIA-SEQUENCE:0\n") {
					t.Fatalf("source-aligned sequence does not include the omitted prefix: %s", aligned)
				}
				var prefix float64
				gap := false
				for line := range strings.SplitSeq(string(aligned), "\n") {
					if line == "#EXT-X-GAP" {
						gap = true
					}
					if strings.HasPrefix(line, "#EXTINF:") {
						if !gap {
							break
						}
						duration, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(line, "#EXTINF:"), ","), 64)
						if err != nil {
							t.Fatal(err)
						}
						prefix += duration
						gap = false
					}
				}
				var packets struct {
					Packets []struct {
						PTS string `json:"pts_time"`
					} `json:"packets"`
				}
				raw := run(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_packets",
					"-show_entries", "packet=pts_time", "-of", "json", filepath.Join(output, "stream.m3u8"))
				if err := json.Unmarshal(raw, &packets); err != nil {
					t.Fatal(err)
				}
				if len(packets.Packets) == 0 {
					t.Fatal("no video packets")
				}
				first, err := strconv.ParseFloat(packets.Packets[0].PTS, 64)
				if err != nil {
					t.Fatal(err)
				}
				if math.Abs(first-prefix) > 0.002 {
					t.Fatalf("source origin mismatch: requested=%g anchor=%g gap-prefix=%g first-fragment-PTS=%g", requested, anchor, prefix, first)
				}
				closest := math.Inf(1)
				for _, packet := range packets.Packets {
					pts, err := strconv.ParseFloat(packet.PTS, 64)
					if err != nil {
						t.Fatal(err)
					}
					closest = min(closest, math.Abs(pts-requested))
				}
				if closest > 0.5 {
					t.Fatalf("requested source time %g absent from fragments: nearest distance=%g", requested, closest)
				}
				t.Logf("requested=%g anchor=%g firstPTS=%g prefix=%g segment=%d nearest-target=%g", requested, anchor, first, prefix, segment, closest)
			})
		}
	}
}
