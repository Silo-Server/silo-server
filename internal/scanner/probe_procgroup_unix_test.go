//go:build unix

package scanner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A wrapper script's ffprobe dies with the wrapper, so stalled probes do not
// pile up outside the scan's workers, one per scan. The deadline and a
// canceled scan end the run through the same cancellation, which the test
// triggers once the wrapper has started its child, so a slow start on a
// loaded host cannot beat it.
func TestProbeFileKillsTheWrappersChildWithIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	ffprobePath := filepath.Join(dir, "ffprobe")
	writeFakeTool(t, ffprobePath, "#!/bin/sh\nsleep 20 &\necho $! > '"+pidFile+"'\nwait\n")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := probeFileWithin(ctx, ffprobePath, filepath.Join(dir, "S01E06.mkv"), probeLimits{probe: time.Minute, waitDelay: 200 * time.Millisecond})
		done <- err
	}()
	pid := waitForPID(t, pidFile)
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("ffprobe is still running after its context ended")
	}

	// The killed child is gone once it is reaped, and a zombie until then.
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err != nil || strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wrapper's child %d is still running (%s)", pid, strings.TrimSpace(string(state)))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForPID waits for the fake wrapper to write its child's pid to path.
func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(data), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("child pid %q: %v", data, err)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake wrapper never started its child")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
