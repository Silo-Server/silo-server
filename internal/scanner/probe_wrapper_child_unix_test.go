//go:build unix

package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/procgroup/procgrouptest"
)

// A wrapper script's ffprobe dies with the wrapper, so stalled probes do not
// pile up outside the scan's workers, one per scan. A canceled scan ends the
// run here; the test cancels once the wrapper has started its child, so a slow
// start on a loaded host cannot beat it.
func TestProbeFileKillsTheWrappersChildWithIt(t *testing.T) {
	t.Parallel()
	wrapper := procgrouptest.WriteWrapper(t, "ffprobe")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := probeFileWithin(ctx, wrapper.Path, filepath.Join(t.TempDir(), "S01E06.mkv"), probeLimits{probe: time.Minute, waitDelay: 200 * time.Millisecond})
		done <- err
	}()
	pid := wrapper.ChildPID(t)
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("ffprobe is still running after its context ended")
	}
	procgrouptest.RequireExited(t, pid)
}

// The probe deadline, not only a canceled scan, kills the wrapper's child.
func TestProbeFileDeadlineKillsTheWrappersChild(t *testing.T) {
	t.Parallel()
	wrapper := procgrouptest.WriteWrapper(t, "ffprobe")

	done := make(chan error, 1)
	go func() {
		_, err := probeFileWithin(t.Context(), wrapper.Path, filepath.Join(t.TempDir(), "S01E06.mkv"), probeLimits{probe: 3 * time.Second, waitDelay: 200 * time.Millisecond})
		done <- err
	}()
	pid := wrapper.ChildPID(t)
	if err := <-done; !errors.Is(err, errProbeTimeout) {
		t.Fatalf("error %v, want %v", err, errProbeTimeout)
	}
	procgrouptest.RequireExited(t, pid)
}

// The packet scan reads ffprobe's output before Wait; its deadline kills the
// wrapper's child as well.
func TestPacketScanDeadlineKillsTheWrappersChild(t *testing.T) {
	t.Parallel()
	wrapper := procgrouptest.WriteWrapper(t, "ffprobe")

	done := make(chan error, 1)
	go func() {
		_, err := probeVideoPacketDuration(t.Context(), wrapper.Path, filepath.Join(t.TempDir(), "S01E06.mkv"), "24/1", 3*time.Second, 200*time.Millisecond)
		done <- err
	}()
	pid := wrapper.ChildPID(t)
	if err := <-done; !errors.Is(err, errProbeTimeout) {
		t.Fatalf("error %v, want %v", err, errProbeTimeout)
	}
	procgrouptest.RequireExited(t, pid)
}
