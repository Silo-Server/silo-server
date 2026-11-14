package playback

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

const (
	qsvPairDevA       = "/dev/dri/renderD888"
	qsvPairDevB       = "/dev/dri/renderD889"
	qsvPairConfigured = qsvPairDevA + "," + qsvPairDevB
)

// startQSVPairTranscode clears the device load, makes both render devices
// present, and starts a QSV transcode balanced across them.
func startQSVPairTranscode(t *testing.T, sessionID, ffmpegPath, avoidDevice string) (*TranscodeSession, error) {
	t.Helper()
	resetDeviceLoad(t)
	fakeDeviceStat(t, qsvPairDevA, qsvPairDevB)
	return StartTranscode(context.Background(), TranscodeOpts{
		InputPath: "/nonexistent/input.mkv", OutputDir: t.TempDir(), SessionID: sessionID,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		FFmpegPath: ffmpegPath, HWAccel: "qsv", HWDevice: qsvPairConfigured, AvoidHWDevice: avoidDevice,
	})
}

func awaitTranscodeProcessExit(t *testing.T, s *TranscodeSession, what string) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not exit", what)
	}
}

func TestStartTranscodeHoldsGPUReservationUntilProcessExit(t *testing.T) {
	devA := qsvPairDevA
	s, err := startQSVPairTranscode(t, "hwdevice-session", fakeFFmpegScript(t), "")
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
	awaitTranscodeProcessExit(t, s, "ffmpeg process")
	if got := hwDeviceActiveCount(devA); got != 0 {
		t.Fatalf("active count after close = %d, want 0", got)
	}
}

func TestStartTranscodeReleasesReservationOnSpawnFailure(t *testing.T) {
	_, err := startQSVPairTranscode(t, "hwdevice-spawn-fail", filepath.Join(t.TempDir(), "missing-ffmpeg"), "")
	if err == nil {
		t.Fatal("StartTranscode succeeded with a missing ffmpeg binary")
	}
	if got := hwDeviceActiveCount(qsvPairDevA); got != 0 {
		t.Fatalf("active count after failed spawn = %d, want 0", got)
	}
}

func TestRestartReacquiresSameGPUAfterNaturalExit(t *testing.T) {
	devA, devB := qsvPairDevA, qsvPairDevB
	s, err := startQSVPairTranscode(t, "hwdevice-restart", restartableFakeFFmpegScript(t), "")
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = s.CloseProcess() })
	awaitTranscodeProcessExit(t, s, "initial ffmpeg process")

	occupiedDevice, releaseOccupant := AcquireHWDevice(qsvPairConfigured, "qsv")
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

func TestAvoidedStartupDeviceKeepsReservationAcrossRestart(t *testing.T) {
	devB := qsvPairDevB
	s, err := startQSVPairTranscode(t, "hwdevice-avoid-restart", restartableFakeFFmpegScript(t), qsvPairDevA)
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = s.CloseProcess() })
	awaitTranscodeProcessExit(t, s, "initial ffmpeg process")
	if got := s.Opts().HWDevice; got != devB {
		t.Fatalf("retry session device = %q, want alternate device %q", got, devB)
	}
	if got := hwDeviceActiveCount(devB); got != 0 {
		t.Fatalf("active count after initial exit = %d, want 0", got)
	}

	if err := s.Restart(context.Background(), 10, 5); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := s.Opts().HWDevice; got != devB {
		t.Fatalf("restart device = %q, want alternate device %q", got, devB)
	}
	if got := hwDeviceActiveCount(devB); got != 1 {
		t.Fatalf("active count while restarted process runs = %d, want 1", got)
	}

	if err := s.CloseProcess(); err != nil {
		t.Fatalf("CloseProcess: %v", err)
	}
	if got := hwDeviceActiveCount(devB); got != 0 {
		t.Fatalf("active count after restarted process exit = %d, want 0", got)
	}
}

func TestRestartReleasesReservationOnSpawnFailure(t *testing.T) {
	bin := restartableFakeFFmpegScript(t)
	s, err := startQSVPairTranscode(t, "hwdevice-restart-spawn-fail", bin, "")
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	awaitTranscodeProcessExit(t, s, "initial ffmpeg process")
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove fake ffmpeg: %v", err)
	}

	if err := s.Restart(context.Background(), 10, 5); err == nil {
		t.Fatal("Restart succeeded with a missing ffmpeg binary")
	}
	if got := hwDeviceActiveCount(qsvPairDevA); got != 0 {
		t.Fatalf("active count after failed restart spawn = %d, want 0", got)
	}
	if err := s.CloseProcess(); err != nil {
		t.Fatalf("CloseProcess: %v", err)
	}
}

// hwaccelReinitFailingFFmpeg writes a fake FFmpeg that records each
// invocation's arguments under logDir and, whenever it is asked to decode on
// the GPU, exits the way an Intel transcode does when the VAAPI decoder
// rebuilds its frames context mid-stream. Without -hwaccel it runs until
// killed.
func hwaccelReinitFailingFFmpeg(t *testing.T, logDir string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := `#!/bin/sh
n=1
while [ -e "` + logDir + `/args-$n" ]; do n=$((n+1)); done
printf '%s\n' "$@" > "` + logDir + `/args-$n.tmp"
mv "` + logDir + `/args-$n.tmp" "` + logDir + `/args-$n"
for arg in "$@"; do
  if [ "$arg" = "-hwaccel" ]; then
    cat >&2 <<'EOF'
Impossible to convert between the formats supported by the filter 'Parsed_sidedata_10' and the filter 'auto_scale_0'
[vf#0:0 @ 0x5ad071e0c0c0] Error reinitializing filters!
[vf#0:0 @ 0x5ad071e0c0c0] Task finished with error code: -38 (Function not implemented)
[vost#0:0/h264_vaapi @ 0x5ad071df5a80] [enc:h264_vaapi @ 0x5ad071df2680] Could not open encoder before EOF
EOF
    exit 187
  fi
done
while :; do sleep 0.1; done
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A transcode whose GPU decode cannot survive a filter graph rebuild fails the
// same way on every restart, so the restart must move the session to CPU
// decode instead of launching the same command again.
func TestRestartDecodesOnCPUAfterHardwareFilterReinitFailure(t *testing.T) {
	for _, hwAccel := range []string{"qsv", "vaapi"} {
		t.Run(hwAccel, func(t *testing.T) {
			resetDeviceLoad(t)
			fakeDeviceStat(t, qsvPairDevA)
			logDir := t.TempDir()
			s, err := StartTranscode(context.Background(), TranscodeOpts{
				InputPath: "/nonexistent/input.mkv", OutputDir: t.TempDir(), SessionID: "reinit-" + hwAccel,
				SourceVideoCodec: "hevc", SourceVideoProfile: "Main 10", SourceVideoBitDepth: 10,
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
				FFmpegPath: hwaccelReinitFailingFFmpeg(t, logDir), HWAccel: hwAccel, HWDevice: qsvPairDevA,
			})
			if err != nil {
				t.Fatalf("StartTranscode: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			awaitTranscodeProcessExit(t, s, "initial ffmpeg process")
			if s.WaitError() == nil {
				t.Fatal("initial GPU-decode attempt exited cleanly; the fake FFmpeg did not fail it")
			}
			first := readFFmpegInvocation(t, logDir, 1)
			if !strings.Contains(first, "\n-hwaccel\n") {
				t.Fatalf("initial attempt did not decode on the GPU:\n%s", first)
			}

			if err := s.Restart(context.Background(), 602, 301); err != nil {
				t.Fatalf("Restart: %v", err)
			}
			second := readFFmpegInvocation(t, logDir, 2)
			if strings.Contains(second, "\n-hwaccel\n") {
				t.Fatalf("restart repeated the GPU decode that had just failed:\n%s", second)
			}
			// FFmpeg rotates CPU-decoded frames by the display matrix but never
			// hardware frames, so the replacement must not start rotating. QSV
			// always passes the flag; only the VAAPI row checks new behavior.
			if !strings.Contains(second, "\n-noautorotate\n") {
				t.Fatalf("restart would rotate frames the GPU-decoded segments left unrotated:\n%s", second)
			}
			// A recipe written from the live options (an audio-track switch
			// persists one) must still say GPU decode: a reconstruct that
			// started on CPU decode would rotate what earlier segments did not.
			if NewRecipeCard(1, "profile", 1, "", s.Opts()).SoftwareVideoDecode {
				t.Fatal("the session's fallback leaked into a recipe written from its live options")
			}
			if !s.IsRunning() {
				t.Fatal("restarted transcode is not running")
			}
		})
	}
}

// readFFmpegInvocation returns the newline-separated arguments of the fake
// FFmpeg's n-th invocation, waiting for the process to record them.
func readFFmpegInvocation(t *testing.T, logDir string, n int) string {
	t.Helper()
	path := filepath.Join(logDir, fmt.Sprintf("args-%d", n))
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return "\n" + string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake FFmpeg invocation %d was never recorded: %v", n, err)
		}
		time.Sleep(time.Millisecond) //nolint:forbidigo // Polls observable state: the fake FFmpeg records its arguments from its own process.
	}
}
