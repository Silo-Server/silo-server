//go:build unix

package jellycompat

import (
	"context"
	"errors"
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
