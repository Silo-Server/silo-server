package playback

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A file whose container outlasts its video and audio (here a subtitle cue at
// 28-30 s on 20 s of video, as in #5127) starts an encoded transcode inside
// that tail at the last segment that holds video, and anywhere else where it
// was asked to.
func TestResolveTranscodeStartMovesATailStartToTheLastVideoSegment(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	subtitlePath := filepath.Join(dir, "tail.srt")
	if err := os.WriteFile(subtitlePath, []byte("1\n00:00:28,000 --> 00:00:30,000\nend credits\n"), 0o644); err != nil {
		t.Fatalf("write subtitle: %v", err)
	}
	sourcePath := filepath.Join(dir, "tail.mkv")
	encodeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	encode := exec.CommandContext(encodeCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=20",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-i", subtitlePath,
		"-map", "0:v", "-map", "1:a", "-map", "2:s",
		"-c:v", "mpeg2video", "-c:a", "mp2", "-c:s", "srt",
		"-y", sourcePath,
	)
	if output, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v\n%s", err, output)
	}

	for _, tc := range []struct {
		name      string
		requested float64
		want      float64
	}{
		{name: "inside the tail", requested: 24, want: 18},
		{name: "on the boundary where the video ends", requested: 20.5, want: 18},
		{name: "past the stored duration", requested: 31, want: 18},
		{name: "inside the last video segment", requested: 19, want: 19},
		{name: "mid-file", requested: 10, want: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveTranscodeStart(context.Background(), ffmpegPath, sourcePath, tc.requested, 30, 2)
			if err != nil {
				t.Fatalf("ResolveTranscodeStart: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ResolveTranscodeStart(%v) = %v, want %v", tc.requested, got, tc.want)
			}
		})
	}
}
