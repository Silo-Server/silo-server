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

// restartableFakeFFmpegScript exits cleanly on its first invocation, then
// stays alive on later invocations so restart reservation behavior can be
// observed without running a real transcode.
func restartableFakeFFmpegScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\n" +
		"marker=\"$PWD/ffmpeg-started\"\n" +
		"if [ ! -e \"$marker\" ]; then touch \"$marker\"; exit 0; fi\n" +
		"while :; do sleep 0.1; done\n"
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

func TestStartTranscodeReleasesReservationOnNaturalExit(t *testing.T) {
	resetDeviceLoad(t)
	devA, devB := "/dev/dri/renderD888", "/dev/dri/renderD889"
	fakeDeviceStat(t, devA, devB)

	s, err := StartTranscode(context.Background(), TranscodeOpts{
		InputPath:        "/nonexistent/input.mkv",
		OutputDir:        t.TempDir(),
		SessionID:        "hwdevice-natural-exit",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		FFmpegPath:       restartableFakeFFmpegScript(t),
		HWAccel:          "qsv",
		HWDevice:         devA + "," + devB,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("ffmpeg process did not exit")
	}
	if got := hwDeviceActiveCount(devA); got != 0 {
		t.Fatalf("active count after natural exit = %d, want 0", got)
	}
	if err := s.CloseProcess(); err != nil {
		t.Fatalf("CloseProcess: %v", err)
	}
}

func TestRestartReacquiresSameGPUAfterNaturalExit(t *testing.T) {
	resetDeviceLoad(t)
	devA, devB := "/dev/dri/renderD888", "/dev/dri/renderD889"
	configured := devA + "," + devB
	fakeDeviceStat(t, devA, devB)

	s, err := StartTranscode(context.Background(), TranscodeOpts{
		InputPath:        "/nonexistent/input.mkv",
		OutputDir:        t.TempDir(),
		SessionID:        "hwdevice-restart",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		FFmpegPath:       restartableFakeFFmpegScript(t),
		HWAccel:          "qsv",
		HWDevice:         configured,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = s.CloseProcess() })

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("initial ffmpeg process did not exit")
	}

	occupiedDevice, releaseOccupant := AcquireHWDevice(configured, "qsv")
	defer releaseOccupant()
	if occupiedDevice != devA {
		t.Fatalf("device selected after natural exit = %q, want released device %q", occupiedDevice, devA)
	}

	if err := s.Restart(context.Background(), 10, 5); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := s.Opts().HWDevice; got != devA {
		t.Fatalf("restart device = %q, want original device %q", got, devA)
	}
	if got := hwDeviceActiveCount(devA); got != 2 {
		t.Fatalf("active count while restarted process runs = %d, want 2", got)
	}
	if got := hwDeviceActiveCount(devB); got != 0 {
		t.Fatalf("alternate device active count = %d, want 0", got)
	}

	if err := s.CloseProcess(); err != nil {
		t.Fatalf("CloseProcess: %v", err)
	}
	if got := hwDeviceActiveCount(devA); got != 1 {
		t.Fatalf("active count after restarted process exit = %d, want occupant only (1)", got)
	}
}

func TestRestartReleasesReservationOnSpawnFailure(t *testing.T) {
	resetDeviceLoad(t)
	devA, devB := "/dev/dri/renderD888", "/dev/dri/renderD889"
	fakeDeviceStat(t, devA, devB)
	bin := restartableFakeFFmpegScript(t)

	s, err := StartTranscode(context.Background(), TranscodeOpts{
		InputPath:        "/nonexistent/input.mkv",
		OutputDir:        t.TempDir(),
		SessionID:        "hwdevice-restart-spawn-fail",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		FFmpegPath:       bin,
		HWAccel:          "qsv",
		HWDevice:         devA + "," + devB,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("initial ffmpeg process did not exit")
	}
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove fake ffmpeg: %v", err)
	}

	if err := s.Restart(context.Background(), 10, 5); err == nil {
		t.Fatal("Restart succeeded with a missing ffmpeg binary")
	}
	if got := hwDeviceActiveCount(devA); got != 0 {
		t.Fatalf("active count after failed restart spawn = %d, want 0", got)
	}
	if err := s.CloseProcess(); err != nil {
		t.Fatalf("CloseProcess: %v", err)
	}
}
