package playback

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeFFmpegScript writes a shell script that runs until killed, standing in
// for a long-lived ffmpeg process.
func fakeFFmpegScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStartTranscodeHoldsGPUReservationUntilProcessExit(t *testing.T) {
	resetDeviceLoad(t)
	devA, devB := "/dev/dri/renderD888", "/dev/dri/renderD889"
	fakeDeviceStat(t, devA, devB)

	s, err := StartTranscode(context.Background(), TranscodeOpts{
		InputPath:        "/nonexistent/input.mkv",
		OutputDir:        t.TempDir(),
		SessionID:        "hwdevice-session",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		FFmpegPath:       fakeFFmpegScript(t),
		HWAccel:          "qsv",
		HWDevice:         devA + "," + devB,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}

	if got := s.Opts().HWDevice; got != devA {
		t.Fatalf("session device = %q, want first listed device %q", got, devA)
	}
	if got := hwDeviceActiveCount(devA); got != 1 {
		t.Fatalf("active count while running = %d, want 1", got)
	}

	// CloseProcess kills ffmpeg and waits for it to be reaped; the
	// reservation must survive until that wait completes and be gone after.
	if err := s.CloseProcess(); err != nil {
		t.Fatalf("CloseProcess: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("ffmpeg process did not exit")
	}
	if got := hwDeviceActiveCount(devA); got != 0 {
		t.Fatalf("active count after close = %d, want 0", got)
	}
}

func TestStartTranscodeReleasesReservationOnSpawnFailure(t *testing.T) {
	resetDeviceLoad(t)
	devA, devB := "/dev/dri/renderD888", "/dev/dri/renderD889"
	fakeDeviceStat(t, devA, devB)

	_, err := StartTranscode(context.Background(), TranscodeOpts{
		InputPath:        "/nonexistent/input.mkv",
		OutputDir:        t.TempDir(),
		SessionID:        "hwdevice-spawn-fail",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		FFmpegPath:       filepath.Join(t.TempDir(), "missing-ffmpeg"),
		HWAccel:          "qsv",
		HWDevice:         devA + "," + devB,
	})
	if err == nil {
		t.Fatal("StartTranscode succeeded with a missing ffmpeg binary")
	}
	if got := hwDeviceActiveCount(devA); got != 0 {
		t.Fatalf("active count after failed spawn = %d, want 0", got)
	}
}
