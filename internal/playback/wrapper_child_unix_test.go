//go:build unix

package playback

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/procgroup/procgrouptest"
)

// Stopping a stream ends the real ffmpeg under a wrapper script too, not only
// the wrapper, so a stopped transcode does not keep encoding.
func TestTranscodeCloseKillsTheWrappersChild(t *testing.T) {
	t.Parallel()
	wrapper := procgrouptest.WriteWrapper(t, "ffmpeg")
	s, err := StartTranscode(context.Background(), TranscodeOpts{
		InputPath:        "/media/movie.mkv",
		OutputDir:        t.TempDir(),
		SessionID:        "wrapper-child",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		FFmpegPath:       wrapper.Path,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	pid := wrapper.ChildPID(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	procgrouptest.RequireExited(t, pid)
}

// A deadline-bounded ffmpeg run kills the wrapper's child when it passes the
// deadline.
func TestFFmpegProbeDeadlineKillsTheWrappersChild(t *testing.T) {
	t.Parallel()
	wrapper := procgrouptest.WriteWrapper(t, "ffmpeg")
	done := make(chan error, 1)
	go func() {
		_, err := runFFmpegProbe(context.Background(), 3*time.Second, wrapper.Path, "-hide_banner", "-encoders")
		done <- err
	}()
	pid := wrapper.ChildPID(t)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ffmpeg probe succeeded past its deadline")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ffmpeg probe is still waiting on its output after its deadline")
	}
	procgrouptest.RequireExited(t, pid)
}
