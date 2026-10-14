package scanner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// shortProbeLimits stand in for defaultProbeLimits, so a stalled fake
// ffprobe reaches its deadline in a second, long after the fake has started
// its sleep.
var shortProbeLimits = probeLimits{probe: time.Second, packetsMinimum: time.Second, packetsBytesPerSecond: 1e12, waitDelay: 200 * time.Millisecond}

// escapedSleep is a shell command that sleeps in a session of its own, so
// killing the fake's process group leaves it running and holding the fake's
// output, which only WaitDelay or a pipe close can then let go of.
func escapedSleep(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl is needed to start a sleep outside the process group")
	}
	return `perl -e 'use POSIX; POSIX::setsid(); exec "sleep", "20"'`
}

// probeFinishes runs probeFileWithin in the background and fails the test if
// it is still running long after its deadlines: without them it waits out
// the fake ffprobe's 20-second sleep.
func probeFinishes(t *testing.T, ffprobePath, filePath string, limits probeLimits) (*ProbeData, error) {
	t.Helper()
	type result struct {
		probe *ProbeData
		err   error
	}
	done := make(chan result, 1)
	go func() {
		probe, err := probeFileWithin(t.Context(), ffprobePath, filePath, limits)
		done <- result{probe, err}
	}()
	select {
	case r := <-done:
		return r.probe, r.err
	case <-time.After(15 * time.Second):
		t.Fatal("ffprobe is still running past its deadline")
		return nil, nil
	}
}

// Issue #581: ffprobes stalled on their storage held every scan worker for
// minutes. A stalled probe is now killed at its deadline, and the timeout
// is neither a rejection, which would mark the file unreadable until it
// changes, nor a context error, which scan loops take for the end of the
// whole scan. The sleep runs outside the fake's process group, so it holds
// ffprobe's output open after the kill until WaitDelay closes it.
func TestProbeFileKillsStalledFFprobeAtItsDeadline(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	media := filepath.Join(t.TempDir(), "S01E05.mkv")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffprobePath := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, ffprobePath, "#!/bin/sh\n"+escapedSleep(t)+"\nexit 0\n")

	_, err := probeFinishes(t, ffprobePath, media, shortProbeLimits)
	if !errors.Is(err, errProbeTimeout) {
		t.Fatalf("stalled probe = %v, want errProbeTimeout", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("stalled probe = %v, which reads as the scan's context ending", err)
	}
	if IsProbeRejection(ctx, media, err) {
		t.Fatal("a probe killed at its deadline counted as ffprobe rejecting the file")
	}
}

// A packet scan that stalls is killed at its own deadline, and the probe
// keeps the metadata that already parsed, as after any failed packet scan.
// The stalled scan writes nothing, so its read waits on a pipe the sleep,
// outside the fake's process group, holds open.
func TestProbeFileKillsStalledPacketScanAtItsDeadline(t *testing.T) {
	t.Parallel()
	ffprobePath := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, ffprobePath, `#!/bin/sh
case " $* " in
  *" -show_format "*)
    printf '%s\n' '{"format":{"duration":"4298357.248000","size":"1258000000"},"streams":[{"codec_type":"video","codec_name":"h264","avg_frame_rate":"30/1"}]}'
    ;;
  *)
    `+escapedSleep(t)+`
    exit 0
    ;;
esac
`)
	limits := shortProbeLimits
	limits.probe = time.Minute

	probe, err := probeFinishes(t, ffprobePath, "stalled.mkv", limits)
	if err != nil {
		t.Fatalf("ProbeFile() returned error: %v", err)
	}
	if probe.Duration != 0 || probe.CodecVideo != "h264" {
		t.Fatalf("probe duration %d codec %q, want an unknown duration and the parsed codec", probe.Duration, probe.CodecVideo)
	}
	if _, err := probeVideoPacketDuration(t.Context(), ffprobePath, "stalled.mkv", "30/1", time.Second, limits.waitDelay); !errors.Is(err, errProbeTimeout) {
		t.Fatalf("stalled packet scan = %v, want errProbeTimeout", err)
	}
}

// A packet scan reads the whole file: a small or unsized file gets the
// minimum, and a large one the time to read it at the floor rate.
func TestProbeLimitsScalePacketScanWithFileSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		size int64
		want time.Duration
	}{
		{0, 30 * time.Minute},
		{4_000_000_000, 30 * time.Minute},
		{50_000_000_000, 5000 * time.Second},
	} {
		if got := defaultProbeLimits.packetScan(tc.size); got != tc.want {
			t.Errorf("packetScan(%d) = %s, want %s", tc.size, got, tc.want)
		}
	}
}
