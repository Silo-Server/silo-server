package playback

import (
	"context"
	"encoding/binary"
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

func TestHEVCFMP4TimestampsRealFFmpeg(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	run := func(t *testing.T, binary string, args ...string) []byte {
		t.Helper()
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(binary), err, output)
		}
		return output
	}
	if !strings.Contains(string(run(t, ffmpeg, "-hide_banner", "-encoders")), "libx265") {
		t.Skip("libx265 is unavailable")
	}
	source := filepath.Join(t.TempDir(), "source.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "10",
		"-c:v", "ffv1", "-c:a", "pcm_s16le", source)
	for _, seek := range []float64{0, 6} {
		t.Run(fmt.Sprintf("seek-%g", seek), func(t *testing.T) {
			dir := t.TempDir()
			opts := TranscodeOpts{
				InputPath: source, OutputDir: dir, FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
				SourceVideoCodec: "ffv1", TargetCodecVideo: transcodeCodecHEVC, TargetCodecAudio: "aac",
				VideoSampleEntry: VideoSampleEntryHVC1, SeekSeconds: seek, SegmentDuration: 2,
				StartSegmentNumber: int(seek) / 2,
			}
			run(t, ffmpeg, buildFFmpegArgs(opts)...)
			fragment, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("seg_%05d.m4s", opts.StartSegmentNumber)))
			if err != nil {
				t.Fatal(err)
			}
			decodeTimes := hevcFragmentDecodeTimes(t, fragment)
			if len(decodeTimes) != 2 {
				t.Fatalf("first fragment has %d decode times, want video and audio", len(decodeTimes))
			}
			for track, value := range decodeTimes {
				if value > math.MaxInt64 {
					t.Errorf("track %d has a wrapped negative tfdt: %#x (signed %d); Media3 rejects its sign bit", track, value, int64(value))
				}
			}
			manifest := filepath.Join(dir, "stream.m3u8")
			var report struct {
				Packets []struct {
					Type string `json:"codec_type"`
					PTS  string `json:"pts_time"`
				} `json:"packets"`
			}
			output := run(t, ffprobe, "-v", "error", "-show_packets", "-show_entries", "packet=codec_type,pts_time", "-of", "json", manifest)
			if err := json.Unmarshal(output, &report); err != nil {
				t.Fatal(err)
			}
			firstPTS := map[string]float64{}
			for _, packet := range report.Packets {
				if _, exists := firstPTS[packet.Type]; exists {
					continue
				}
				pts, err := strconv.ParseFloat(packet.PTS, 64)
				if err != nil {
					t.Fatal(err)
				}
				firstPTS[packet.Type] = pts
			}
			for _, track := range []string{"video", "audio"} {
				pts, exists := firstPTS[track]
				// Startup may shift both streams by the small encoder delay;
				// a seek must keep its source timestamp instead of resetting.
				if !exists || math.Abs(pts-seek) > 0.15 {
					t.Errorf("%s first PTS=%g (present=%t), want source time near %g", track, pts, exists, seek)
				}
			}
			if skew := math.Abs(firstPTS["video"] - firstPTS["audio"]); skew > 0.1 {
				t.Errorf("first audio/video packets differ by %gs", skew)
			}
			run(t, ffmpeg, "-v", "error", "-xerror", "-i", manifest, "-f", "null", "-")
			t.Logf("seek=%g tfdt=%v firstPTS=%v", seek, decodeTimes, firstPTS)
		})
	}
}

// Read the ISO-BMFF boxes directly: ffprobe accepts signed timestamps that
// Media3 rejects, so successful probing alone cannot verify this contract.
func hevcFragmentDecodeTimes(t *testing.T, data []byte) []uint64 {
	t.Helper()
	var times []uint64
	for len(data) > 0 {
		if len(data) < 8 {
			t.Fatal("truncated fragment box header")
		}
		size := int(binary.BigEndian.Uint32(data[:4]))
		if size < 8 || size > len(data) {
			t.Fatalf("invalid fragment box size %d", size)
		}
		payload := data[8:size]
		switch string(data[4:8]) {
		case "moof", "traf":
			times = append(times, hevcFragmentDecodeTimes(t, payload)...)
		case "tfdt":
			if len(payload) < 8 {
				t.Fatal("truncated tfdt box")
			}
			switch payload[0] {
			case 0:
				times = append(times, uint64(binary.BigEndian.Uint32(payload[4:8])))
			case 1:
				if len(payload) < 12 {
					t.Fatal("truncated version 1 tfdt box")
				}
				times = append(times, binary.BigEndian.Uint64(payload[4:12]))
			default:
				t.Fatalf("unexpected tfdt version %d", payload[0])
			}
		}
		data = data[size:]
	}
	return times
}
