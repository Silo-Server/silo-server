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
	Path       string
	pidFile    string
	leaderFile string
}

// WriteWrapper writes a Wrapper named name in a new temporary directory.
func WriteWrapper(t testing.TB, name string) Wrapper {
	t.Helper()
	return writeWrapper(t, name, true)
}

// WriteExitingWrapper writes a Wrapper named name that exits right after it
// starts its child, leaving the child running and holding the wrapper's
// stdout and stderr, the way npm can exit while webpack still runs.
func WriteExitingWrapper(t testing.TB, name string) Wrapper {
	t.Helper()
	return writeWrapper(t, name, false)
}

func writeWrapper(t testing.TB, name string, wait bool) Wrapper {
	t.Helper()
	dir := t.TempDir()
	w := Wrapper{
		Path:       filepath.Join(dir, name),
		pidFile:    filepath.Join(dir, name+".child.pid"),
		leaderFile: filepath.Join(dir, name+".pid"),
	}
	script := "#!/bin/sh\necho $$ > '" + w.leaderFile + "'\nsleep 120 &\necho $! > '" + w.pidFile + "'\n"
	if wait {
		script += "wait\n"
	}
	// execve refuses a file another process still has open for writing. A
	// fork landing between this open and its close would hold a copy of the
	// fd until it execs, so hold the lock os/exec takes around fork.
	syscall.ForkLock.Lock()
	err := os.WriteFile(w.Path, []byte(script), 0o755) //nolint:gosec // G306: the wrapper must be executable.
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
	pid := readPID(t, w.pidFile, "child")
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// LeaderPID waits for the wrapper to start and returns its own pid.
func (w Wrapper) LeaderPID(t testing.TB) int {
	t.Helper()
	return readPID(t, w.leaderFile, "own")
}

func readPID(t testing.TB, path, which string) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(data), "\n") { //nolint:gosec // G304: the path is in the test's temporary directory.
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("%s pid %q: %v", which, data, err)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wrapper never wrote its %s pid", which)
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
		state, err := exec.CommandContext(t.Context(), "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // G204: the argument is a pid.
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
