//go:build unix

// Package procgrouptest provides a fake ffmpeg or ffprobe wrapper script for
// tests that check a stopped run also ends the process the wrapper started.
package procgrouptest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Wrapper is an executable script that starts a long sleep as a child, the
// way a wrapper that runs `ffmpeg "$@"` without exec starts the real ffmpeg,
// records the child's pid, and waits for it.
type Wrapper struct {
	// Path is the script, to configure as the ffmpeg or ffprobe path.
	Path    string
	pidFile string
}

// WriteWrapper writes a Wrapper named name in a new temporary directory.
func WriteWrapper(t testing.TB, name string) Wrapper {
	t.Helper()
	dir := t.TempDir()
	w := Wrapper{Path: filepath.Join(dir, name), pidFile: filepath.Join(dir, name+".child.pid")}
	script := "#!/bin/sh\nsleep 120 &\necho $! > '" + w.pidFile + "'\nwait\n"
	// execve refuses a file another process still has open for writing. A
	// fork landing between this open and its close would hold a copy of the
	// fd until it execs, so hold the lock os/exec takes around fork.
	syscall.ForkLock.Lock()
	err := os.WriteFile(w.Path, []byte(script), 0o755)
	syscall.ForkLock.Unlock()
	if err != nil {
		t.Fatalf("writing wrapper %s: %v", w.Path, err)
	}
	return w
}

// ChildPID waits for the wrapper to start its child and returns the child's
// pid. The child is killed when the test ends, so a failing test does not
// leave it running.
func (w Wrapper) ChildPID(t testing.TB) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if data, err := os.ReadFile(w.pidFile); err == nil && strings.HasSuffix(string(data), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("child pid %q: %v", data, err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the wrapper never started its child")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// RequireExited fails the test unless pid exits within 10 seconds. A killed
// child counts as exited once it is a zombie waiting to be reaped.
func RequireExited(t testing.TB, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("reading the state of %d: %v", pid, err)
		}
		if err != nil || strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wrapper's child %d is still running (%s)", pid, strings.TrimSpace(string(state)))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
