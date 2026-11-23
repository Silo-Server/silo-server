//go:build unix

package jellycompat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/procgroup/procgrouptest"
)

// When the install's context ends, the runner stops the processes npm started
// as well, so it returns promptly, and its error says why the context ended.
func TestWebInstallCommandRunnerStopsTheTreeWhenTheInstallEnds(t *testing.T) {
	t.Parallel()
	build := procgrouptest.WriteWrapper(t, "npm")
	run := webInstallCommandRunner(t.TempDir(), "")
	timedOut := errors.New("the install timed out")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	done := make(chan error, 1)
	go func() { done <- run(ctx, "", []string{build.Path}, "") }()
	pid := build.ChildPID(t)
	cancel(timedOut)

	select {
	case err := <-done:
		if !errors.Is(err, timedOut) {
			t.Fatalf("error %v does not name why the install ended", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the runner kept waiting for the build's child after the install ended")
	}
	procgrouptest.RequireExited(t, pid)
}

// When npm exits and leaves a child running, the runner stops the child
// rather than returning while it still builds.
func TestWebInstallCommandRunnerStopsWhatACommandLeftRunning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "npm")
	body := "#!/bin/sh\nsleep 120 &\necho $! > '" + pidFile + "'\nexit 1\n"
	syscall.ForkLock.Lock() // see procgrouptest.WriteWrapper
	err := os.WriteFile(script, []byte(body), 0o755)
	syscall.ForkLock.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	err = webInstallCommandRunner(t.TempDir(), "")(t.Context(), "", []string{script}, "")
	if err == nil {
		t.Fatal("the failing command reported success")
	}
	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if convErr != nil {
		t.Fatal(convErr)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	procgrouptest.RequireExited(t, pid)
}
