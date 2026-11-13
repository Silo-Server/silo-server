package playback

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type recordedPrepareProgress struct {
	mu       sync.Mutex
	readings []PrepareProgress
}

func (r *recordedPrepareProgress) PrepareProgress(p PrepareProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readings = append(r.readings, p)
}

func (r *recordedPrepareProgress) all() []PrepareProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.readings)
}

type recordedFFmpegLog struct {
	sessionID string
	line      string
	event     string
	attrs     FFmpegLogAttrs
}

type recordingFFmpegLogSink struct {
	mu      sync.Mutex
	entries []recordedFFmpegLog
}

func (s *recordingFFmpegLogSink) WriteLine(_ context.Context, sessionID string, attrs FFmpegLogAttrs, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, recordedFFmpegLog{sessionID: sessionID, line: line, attrs: attrs})
}

func (s *recordingFFmpegLogSink) WriteEvent(_ context.Context, sessionID string, attrs FFmpegLogAttrs, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, recordedFFmpegLog{sessionID: sessionID, event: message, attrs: attrs})
}

func TestPrepareProgressWriterParsesBlocksAcrossWrites(t *testing.T) {
	sink := &recordedPrepareProgress{}
	w := newPrepareProgressWriter(sink, 100, 0)
	stream := "frame=10\nout_time_us=N/A\nspeed=N/A\nprogress=continue\n" +
		"out_time_us=25000000\nspeed=3.4x\nprogress=continue\n" +
		"out_time_ms=50500000\nspeed= 41x\nprogress=end\n"
	// Split mid-line so the writer has to buffer partial lines.
	for _, chunk := range []string{stream[:17], stream[17:60], stream[60:]} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	got := sink.all()
	want := []PrepareProgress{
		{EncodedSeconds: 0, DurationSeconds: 100, Speed: 0},
		{EncodedSeconds: 25, DurationSeconds: 100, Speed: 3.4},
		// progress=end reports the whole duration even when out_time lags.
		{EncodedSeconds: 100, DurationSeconds: 100, Speed: 41},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("readings = %+v, want %+v", got, want)
	}
}

func TestPrepareProgressWriterUnknownDuration(t *testing.T) {
	sink := &recordedPrepareProgress{}
	w := newPrepareProgressWriter(sink, -5, 0)
	_, _ = w.Write([]byte("out_time_us=1000000\nprogress=end\n"))
	got := sink.all()
	if len(got) != 1 || got[0].DurationSeconds != 0 || got[0].EncodedSeconds != 1 {
		t.Fatalf("readings = %+v, want one reading at 1s with unknown duration", got)
	}
}

// Issue #3495: FFmpeg's out_time and speed wait for a subtitle track that has
// no cue yet, so they stay at 0 while the encode runs. With the frame rate
// known, a reading follows the frames encoded, and the speed FFmpeg's frame
// rate, until out_time catches up.
func TestPrepareProgressWriterFollowsFramesWhileOutTimeWaits(t *testing.T) {
	sink := &recordedPrepareProgress{}
	w := newPrepareProgressWriter(sink, 600, 24)
	_, _ = w.Write([]byte("frame=2400\nfps=192.0\nout_time_us=0\nspeed=0x\nprogress=continue\n" +
		"frame=9648\nfps=190.0\nout_time_us=402000000\nspeed=7.9x\nprogress=continue\n" +
		"frame=14400\nfps=191.0\nout_time_us=402000000\nspeed=6.4x\nprogress=continue\n" +
		"frame=14424\nfps=191.0\nout_time_us=600000000\nspeed=7.9x\nprogress=end\n"))
	want := []PrepareProgress{
		{EncodedSeconds: 100, DurationSeconds: 600, Speed: 8},
		// out_time has caught up, so FFmpeg's own reading stands.
		{EncodedSeconds: 402, DurationSeconds: 600, Speed: 7.9},
		{EncodedSeconds: 600, DurationSeconds: 600, Speed: 191.0 / 24},
		// The frames never take a reading past the duration.
		{EncodedSeconds: 600, DurationSeconds: 600, Speed: 7.9},
	}
	if got := sink.all(); !slices.Equal(got, want) {
		t.Fatalf("readings = %+v, want %+v", got, want)
	}

	// Without a frame rate, such as for an audio-only file, out_time stands.
	sink = &recordedPrepareProgress{}
	w = newPrepareProgressWriter(sink, 600, 0)
	_, _ = w.Write([]byte("frame=2400\nfps=192.0\nout_time_us=0\nspeed=0x\nprogress=continue\n"))
	if got := sink.all(); len(got) != 1 || got[0].EncodedSeconds != 0 || got[0].Speed != 0 {
		t.Fatalf("readings without a frame rate = %+v, want out_time's", got)
	}
}

// On variable-rate video the average frame rate turns frames into the share
// of frames encoded, which runs ahead of time when most frames come early. It
// stays below the duration until the last frame, and out_time takes over once
// it is further.
func TestPrepareProgressWriterFramesOnUnevenFrameRate(t *testing.T) {
	// 600 s at an average of 24 fps is 14400 frames. 90% of them, 12960,
	// fall in the first 300 s (43.2 fps), the rest in the last 300 s (4.8 fps).
	sink := &recordedPrepareProgress{}
	w := newPrepareProgressWriter(sink, 600, 24)
	_, _ = w.Write([]byte(
		// The first half is encoded while a sparse subtitle holds out_time at 0.
		"frame=12960\nfps=240.0\nout_time_us=0\nspeed=0x\nprogress=continue\n" +
			// One frame short of the end, the reading is still short of the duration.
			"frame=14399\nfps=200.0\nout_time_us=0\nspeed=0x\nprogress=continue\n" +
			// out_time further than the frames stands.
			"frame=14399\nfps=200.0\nout_time_us=599990000\nspeed=4.5x\nprogress=continue\n" +
			"frame=14400\nfps=200.0\nout_time_us=600000000\nspeed=4.5x\nprogress=end\n"))
	got := sink.all()
	want := []PrepareProgress{
		// 90% read after half the time.
		{EncodedSeconds: 540, DurationSeconds: 600, Speed: 10},
		{EncodedSeconds: 14399.0 / 24, DurationSeconds: 600, Speed: 200.0 / 24},
		{EncodedSeconds: 599.99, DurationSeconds: 600, Speed: 4.5},
		{EncodedSeconds: 600, DurationSeconds: 600, Speed: 4.5},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("readings = %+v, want %+v", got, want)
	}
	if got[1].EncodedSeconds >= got[1].DurationSeconds {
		t.Fatalf("reading one frame short of the end = %v, want below the duration", got[1].EncodedSeconds)
	}
}

func TestFileFrameRate(t *testing.T) {
	for _, tc := range []struct {
		name string
		file *models.MediaFile
		want float64
	}{
		{"fraction", &models.MediaFile{VideoTracks: []models.VideoTrack{{FrameRate: "24000/1001"}}}, 24000.0 / 1001},
		{"decimal", &models.MediaFile{VideoTracks: []models.VideoTrack{{FrameRate: "25"}}}, 25},
		{"unknown", &models.MediaFile{VideoTracks: []models.VideoTrack{{FrameRate: "0/0"}}}, 0},
		{"no video", &models.MediaFile{}, 0},
		{"no file", nil, 0},
	} {
		if got := FileFrameRate(tc.file); got != tc.want {
			t.Errorf("%s: FileFrameRate = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildPrepareFileArgsRequestsProgressOnlyWithSink(t *testing.T) {
	opts := TranscodeOpts{InputPath: "/in.mkv", TargetCodecVideo: "copy", TargetCodecAudio: "copy"}
	if args := buildPrepareFileArgs(opts, "/out.mp4"); slices.Contains(args, "-progress") {
		t.Fatalf("args without a sink request progress: %v", args)
	}
	opts.PrepareProgressSink = &recordedPrepareProgress{}
	args := buildPrepareFileArgs(opts, "/out.mp4")
	i := slices.Index(args, "-progress")
	if i < 0 || i+1 >= len(args) || args[i+1] != "pipe:1" || !slices.Contains(args, "-nostats") {
		t.Fatalf("args with a sink = %v, want -progress pipe:1 -nostats", args)
	}
	if args[len(args)-1] != "/out.mp4" {
		t.Fatalf("output path moved: %v", args)
	}
}

func TestPrepareFileReportsProgressAndLogsStderr(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	body := "#!/bin/sh\n" +
		"printf 'out_time_us=30000000\\nspeed=2.0x\\nprogress=continue\\n'\n" +
		"printf 'warning: something odd\\n' >&2\n" +
		"printf 'out_time_us=60000000\\nspeed=2.5x\\nprogress=end\\n'\n" +
		"eval \"touch \\${$#}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	progress := &recordedPrepareProgress{}
	logs := &recordingFFmpegLogSink{}
	err := PrepareFile(context.Background(), TranscodeOpts{
		InputPath:           "/nonexistent/input.mkv",
		TargetCodecVideo:    "h264",
		TargetCodecAudio:    "aac",
		FFmpegPath:          script,
		HWAccel:             HWAccelNone,
		TotalDuration:       60,
		SessionID:           DownloadPrepareLogSessionID("art1"),
		FFmpegLogSink:       logs,
		PrepareProgressSink: progress,
	}, filepath.Join(dir, "artifact.mp4"))
	if err != nil {
		t.Fatalf("PrepareFile: %v", err)
	}

	readings := progress.all()
	if len(readings) != 2 || readings[0].EncodedSeconds != 30 || readings[1].EncodedSeconds != 60 || readings[1].Speed != 2.5 {
		t.Fatalf("readings = %+v", readings)
	}

	var lines, events []string
	for _, entry := range logs.entries {
		if entry.sessionID != "download-prepare-art1" {
			t.Fatalf("log session = %q", entry.sessionID)
		}
		if entry.attrs.ExecutionMode != "download_prepare" {
			t.Fatalf("execution mode = %q", entry.attrs.ExecutionMode)
		}
		if entry.line != "" {
			lines = append(lines, entry.line)
		} else {
			events = append(events, entry.event)
		}
	}
	if !slices.Equal(lines, []string{"warning: something odd"}) {
		t.Fatalf("stderr lines = %q", lines)
	}
	if !slices.Equal(events, []string{"ffmpeg process starting", "ffmpeg process exited"}) {
		t.Fatalf("events = %q", events)
	}
}

// Issue #3495 with FFmpeg: a forced subtitle track whose only cue is near the
// end holds FFmpeg's out_time at 0 until that cue. With the frame rate,
// progress rises through the first half of the file.
//
// Only FFmpeg 8.x (Silo ships jellyfin-ffmpeg 8) holds out_time at 0 here, so
// only there does this test tell fixed from unfixed code. FFmpeg 9 moves
// out_time ahead of the video for this source, and the test passes either way.
func TestPrepareFileProgressRisesBeforeASparseSubtitleCue(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes a two-and-a-half-minute source")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found")
	}
	dir := t.TempDir()
	const duration, sparseCue = 150, 140
	cue := func(n, at int, text string) string {
		stamp := func(s int) string { return fmt.Sprintf("00:%02d:%02d,000", s/60, s%60) }
		return fmt.Sprintf("%d\n%s --> %s\n%s\n\n", n, stamp(at), stamp(at+2), text)
	}
	var dense strings.Builder
	for i, at := 1, 5; at < duration; i, at = i+1, at+10 {
		dense.WriteString(cue(i, at, "line"))
	}
	for name, body := range map[string]string{"dense.srt": dense.String(), "sparse.srt": cue(1, sparseCue, "forced")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(dir, "source.mkv")
	build := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24", "-f", "lavfi", "-i", "sine=frequency=440",
		"-i", filepath.Join(dir, "dense.srt"), "-i", filepath.Join(dir, "sparse.srt"),
		"-t", strconv.Itoa(duration), "-map", "0", "-map", "1", "-map", "2", "-map", "3",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-c:a", "aac", "-c:s", "srt", "-y", source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make source: %v\n%s", err, out)
	}

	progress := &recordedPrepareProgress{}
	err = PrepareFile(t.Context(), TranscodeOpts{
		InputPath:        source,
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		FFmpegPath:       ffmpeg,
		HWAccel:          HWAccelNone,
		TotalDuration:    duration,
		SourceFrameRate:  24,
		PreparedTracks: &PreparedTracks{
			Audio:     []PreparedAudioTrack{{SourceIndex: 0, Codec: PreparedAudioAAC, SourceChannels: 1}},
			Subtitles: []PreparedSubtitleTrack{{SourceIndex: 0}, {SourceIndex: 1, Forced: true}},
		},
		PrepareProgressSink: progress,
	}, filepath.Join(dir, "artifact.mp4"))
	if err != nil {
		t.Fatalf("PrepareFile: %v", err)
	}
	readings := progress.all()
	if len(readings) < 3 {
		t.Skipf("the encode finished in %d readings, too fast to watch", len(readings))
	}
	firstHalf := 0
	for i, reading := range readings {
		if i > 0 && reading.EncodedSeconds < readings[i-1].EncodedSeconds {
			t.Fatalf("progress went back: %+v", readings)
		}
		if reading.EncodedSeconds > 0 && reading.EncodedSeconds < sparseCue/2 {
			firstHalf++
		}
	}
	if firstHalf == 0 {
		t.Fatalf("no reading between 0 and %ds, half the sparse cue's time: %+v", sparseCue/2, readings)
	}
}

// A remux copies the video with the source's own timing, which the probed
// average rate can misstate: on a variable-rate file the frames read past the
// duration with time left. Only an encode reads progress from frames.
func TestPrepareFileReadsFramesOnlyWhenEncodingVideo(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	// 1500 frames at 24 fps is 62.5 s, further than FFmpeg's 30 s.
	body := "#!/bin/sh\n" +
		"printf 'frame=1500\\nfps=240\\nout_time_us=30000000\\nspeed=4.0x\\nprogress=continue\\n'\n" +
		"eval \"touch \\${$#}\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		videoCodec string
		want       float64
	}{
		{"h264", 62.5},
		{"copy", 30},
	} {
		progress := &recordedPrepareProgress{}
		err := PrepareFile(context.Background(), TranscodeOpts{
			InputPath:           "/nonexistent/input.mkv",
			TargetCodecVideo:    tc.videoCodec,
			TargetCodecAudio:    "aac",
			FFmpegPath:          script,
			HWAccel:             HWAccelNone,
			TotalDuration:       120,
			SourceFrameRate:     24,
			PrepareProgressSink: progress,
		}, filepath.Join(dir, tc.videoCodec+".mp4"))
		if err != nil {
			t.Fatalf("%s: PrepareFile: %v", tc.videoCodec, err)
		}
		if got := progress.all(); len(got) != 1 || got[0].EncodedSeconds != tc.want {
			t.Fatalf("%s: readings = %+v, want %vs", tc.videoCodec, got, tc.want)
		}
	}
}
