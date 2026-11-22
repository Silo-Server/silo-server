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

// Issue #3495 with FFmpeg: a forced subtitle track whose only cue is near the
// end holds FFmpeg's out_time at 0 until that cue. With the frame rate,
// progress rises through the first half of the file.
//
// Only FFmpeg 8.x (Silo ships jellyfin-ffmpeg 8) holds out_time at 0 here, so
// only there does this test tell fixed from unfixed code. FFmpeg 9 moves
// out_time ahead of the video for this source, and the test passes either way.
func TestPrepareFileProgressRisesBeforeASparseSubtitleCue(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found")
	}
	dir := t.TempDir()
	const duration, sparseCue = 30, 26
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
	build := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24", "-f", "lavfi", "-i", "sine=frequency=440",
		"-i", filepath.Join(dir, "dense.srt"), "-i", filepath.Join(dir, "sparse.srt"),
		"-t", strconv.Itoa(duration), "-map", "0", "-map", "1", "-map", "2", "-map", "3",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-c:a", "aac", "-c:s", "srt", "-y", source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make source: %v\n%s", err, out)
	}

	// Increase the progress report frequency so a small fixture still gives
	// intermediate readings on fast hosts.
	wrapper := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec ffmpeg -stats_period 0.001 \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	progress := &recordedPrepareProgress{}
	err = PrepareFile(t.Context(), TranscodeOpts{
		InputPath:        source,
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		FFmpegPath:       wrapper,
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
		t.Fatalf("the encode emitted %d progress readings, want at least three", len(readings))
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
