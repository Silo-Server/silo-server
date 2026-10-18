package playback

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
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
	w := newPrepareProgressWriter(sink, 100)
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
	w := newPrepareProgressWriter(sink, -5)
	_, _ = w.Write([]byte("out_time_us=1000000\nprogress=end\n"))
	got := sink.all()
	if len(got) != 1 || got[0].DurationSeconds != 0 || got[0].EncodedSeconds != 1 {
		t.Fatalf("readings = %+v, want one reading at 1s with unknown duration", got)
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

func TestPrepareFileLogsExitErrorWithoutSessionIDLogsNothing(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'boom\\n' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := TranscodeOpts{
		InputPath:        "/nonexistent/input.mkv",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		FFmpegPath:       script,
		HWAccel:          HWAccelNone,
	}

	logs := &recordingFFmpegLogSink{}
	opts.FFmpegLogSink = logs
	if err := PrepareFile(context.Background(), opts, filepath.Join(dir, "a.mp4")); err == nil {
		t.Fatal("PrepareFile succeeded with a failing FFmpeg")
	}
	if len(logs.entries) != 0 {
		t.Fatalf("logged without a session id: %+v", logs.entries)
	}

	opts.SessionID = DownloadPrepareLogSessionID("art2")
	if err := PrepareFile(context.Background(), opts, filepath.Join(dir, "b.mp4")); err == nil {
		t.Fatal("PrepareFile succeeded with a failing FFmpeg")
	}
	last := logs.entries[len(logs.entries)-1]
	if last.event != "ffmpeg process exit error" || !strings.Contains(last.attrs.ExitError, "exit_code=3") {
		t.Fatalf("last entry = %+v, want exit error with code 3", last)
	}
	if !slices.ContainsFunc(logs.entries, func(e recordedFFmpegLog) bool { return e.line == "boom" }) {
		t.Fatalf("stderr line missing: %+v", logs.entries)
	}
}

func TestDownloadPrepareLogSessionID(t *testing.T) {
	if got := DownloadPrepareLogSessionID(" "); got != "" {
		t.Fatalf("blank id = %q", got)
	}
	if got := DownloadPrepareLogSessionID("abc"); got != "download-prepare-abc" {
		t.Fatalf("id = %q", got)
	}
}
