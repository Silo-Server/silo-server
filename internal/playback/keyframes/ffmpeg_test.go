package keyframes

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These tests check the reader and planner against FFmpeg itself, on a file
// with irregular keyframes. They skip when ffmpeg or ffprobe isn't installed.

func requireFFmpeg(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobe, err = exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	return ffmpeg, ffprobe
}

// irregularKeyframes are the forced keyframe times of the test file, with
// gaps both shorter and longer than the 2s segment length.
var irregularKeyframes = []float64{0, 0.9, 2.2, 2.6, 5.2, 7.2, 7.6, 8.9, 12.6, 13.0, 15.0, 17.6, 18.5}

func makeTestMKV(t *testing.T, ffmpeg string) string {
	t.Helper()
	times := make([]string, len(irregularKeyframes))
	for i, k := range irregularKeyframes {
		times[i] = strconv.FormatFloat(k, 'f', -1, 64)
	}
	path := filepath.Join(t.TempDir(), "in.mkv")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "20", "-c:v", "libx264", "-preset", "ultrafast",
		"-g", "1000", "-sc_threshold", "0", "-bf", "0",
		"-force_key_frames", strings.Join(times, ","),
		"-c:a", "aac", path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg can't make the test file (%v): %s", err, out)
	}
	return path
}

func readIndex(t *testing.T, path string) Index {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	idx, err := ReadMatroska(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestReadMatroskaMatchesFFprobe(t *testing.T) {
	ffmpeg, ffprobe := requireFFmpeg(t)
	path := makeTestMKV(t, ffmpeg)

	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Packets []struct {
			PTS   string `json:"pts_time"`
			Flags string `json:"flags"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	var want []float64
	for _, p := range probe.Packets {
		if strings.Contains(p.Flags, "K") {
			v, _ := strconv.ParseFloat(p.PTS, 64)
			want = append(want, v)
		}
	}

	got := readIndex(t, path).Keyframes
	if len(got) != len(want) {
		t.Fatalf("keyframes = %v, ffprobe = %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 0.001 {
			t.Fatalf("keyframes = %v, ffprobe = %v", got, want)
		}
	}
}

// The plan must match the segments FFmpeg's HLS muxer writes for copied
// video, or the complete playlist would list durations the segments don't
// have.
func TestPlanSegmentsMatchesFFmpegHLS(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	// Regular 2s keyframes on a timeline that starts at 0.28s land exactly
	// on every target, which float error in the planner must not skip.
	regular := filepath.Join(t.TempDir(), "regular.mkv")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "10", "-c:v", "libx264", "-preset", "ultrafast",
		"-g", "50", "-sc_threshold", "0", "-bf", "0",
		"-c:a", "aac", "-output_ts_offset", "0.28", regular).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make the test file (%v): %s", err, out)
	}
	for name, path := range map[string]string{"irregular": makeTestMKV(t, ffmpeg), "regular with an offset": regular} {
		t.Run(name, func(t *testing.T) { checkPlanMatchesFFmpegHLS(t, ffmpeg, path) })
	}
}

func checkPlanMatchesFFmpegHLS(t *testing.T, ffmpeg, path string) {
	dir := t.TempDir()
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-i", path, "-map", "0:v:0", "-map", "0:a:0",
		"-copyts", "-avoid_negative_ts", "make_non_negative", "-start_at_zero",
		"-c:v", "copy", "-c:a", "copy",
		"-f", "hls", "-hls_time", "2", "-hls_list_size", "0",
		"-hls_segment_type", "fmp4",
		"-hls_segment_filename", filepath.Join(dir, "seg_%05d.m4s"),
		filepath.Join(dir, "stream.m3u8")).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	written := playlistDurations(t, filepath.Join(dir, "stream.m3u8"))

	idx := readIndex(t, path)
	planned := SegmentDurations(PlanSegments(idx.Keyframes, 2), idx.Duration)
	if len(planned) != len(written) {
		t.Fatalf("planned %d segments %v, FFmpeg wrote %d %v", len(planned), planned, len(written), written)
	}
	// FFmpeg measures the last segment to its last packet's end, which can
	// differ from the container duration by a frame.
	for i := range written[:len(written)-1] {
		if math.Abs(planned[i]-written[i]) > 0.002 {
			t.Fatalf("segment %d: planned %.3fs, FFmpeg wrote %.3fs (planned %v, written %v)",
				i, planned[i], written[i], planned, written)
		}
	}
}

func playlistDurations(t *testing.T, path string) []float64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var durations []float64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if v, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
			d, err := strconv.ParseFloat(strings.TrimSuffix(v, ","), 64)
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			durations = append(durations, d)
		}
	}
	return durations
}
