package playback

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSeparateAudioHLSRealFFmpegKeepsTimestampsAligned(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is unavailable")
	}
	ffprobe := ffprobePathFromFFmpeg(ffmpeg)
	if _, err := exec.LookPath(ffprobe); err != nil {
		t.Skip("ffprobe is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	run := func(binary string, args ...string) []byte {
		t.Helper()
		result, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(binary), err, result)
		}
		return result
	}
	if !strings.Contains(string(run(ffmpeg, "-hide_banner", "-encoders")), "libx264") {
		t.Skip("libx264 is unavailable")
	}
	source := filepath.Join(t.TempDir(), "source.mkv")
	run(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=24", "-f", "lavfi", "-i", "anoisesrc=sample_rate=48000:seed=1234", "-t", "8", "-c:v", "libx264", "-g", "48", "-c:a", "eac3", "-ac", "6", source)
	type packet struct {
		Kind string `json:"codec_type"`
		PTS  string `json:"pts_time"`
		Hash string `json:"data_hash"`
	}
	packets := func(path string) []packet {
		var report struct {
			Packets []packet `json:"packets"`
		}
		if err := json.Unmarshal(run(ffprobe, "-v", "error", "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=codec_type,pts_time,data_hash", "-of", "json", path), &report); err != nil {
			t.Fatal(err)
		}
		return report.Packets
	}
	sourcePackets := map[string]float64{}
	for _, packet := range packets(source) {
		pts, err := strconv.ParseFloat(packet.PTS, 64)
		if err != nil {
			t.Fatal(err)
		}
		sourcePackets[packet.Hash] = pts
	}
	for _, seek := range []float64{0, 4} {
		t.Run(strconv.FormatFloat(seek, 'f', 0, 64), func(t *testing.T) {
			dir := t.TempDir()
			opts := TranscodeOpts{InputPath: source, OutputDir: dir, TargetCodecVideo: "copy", SourceVideoCodec: "h264", TargetCodecAudio: "copy", SeparateAudioHLS: true, SegmentDuration: 2, SeekSeconds: seek, FFmpegPath: ffmpeg}
			session, err := StartTranscode(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			select {
			case <-session.done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			offsets := map[string]float64{}
			for _, name := range []string{"stream.m3u8", "audio.m3u8"} {
				for _, packet := range packets(filepath.Join(dir, name)) {
					if _, seen := offsets[packet.Kind]; seen {
						continue
					}
					original, exists := sourcePackets[packet.Hash]
					if !exists {
						continue
					}
					pts, err := strconv.ParseFloat(packet.PTS, 64)
					if err != nil {
						t.Fatal(err)
					}
					offsets[packet.Kind] = pts - original
				}
			}
			video, videoOK := offsets["video"]
			audio, audioOK := offsets["audio"]
			if !videoOK || !audioOK {
				t.Fatalf("copied packet identity missing: %+v", offsets)
			}
			if math.Abs(video-audio) > 0.005 {
				t.Fatalf("video/audio timestamp shifts differ: %.6f/%.6f", video, audio)
			}
		})
	}
}
