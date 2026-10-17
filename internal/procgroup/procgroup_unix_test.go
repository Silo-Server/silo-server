//go:build unix

package procgroup_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/Silo-Server/silo-server/internal/procgroup"
	"github.com/Silo-Server/silo-server/internal/procgroup/procgrouptest"
)

// A command that exits on its own behaves as with exec.CommandContext: its
// output and exit status come back unchanged.
func TestCommandContextNormalExit(t *testing.T) {
	t.Parallel()
	out, err := procgroup.CommandContext(t.Context(), "sh", "-c", "echo out; echo err >&2").CombinedOutput()
	if err != nil || string(out) != "out\nerr\n" {
		t.Fatalf("output %q error %v", out, err)
	}
	err = procgroup.CommandContext(t.Context(), "sh", "-c", "exit 3").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("error %v, want exit status 3", err)
	}
}

// Kill ends a running command's child as well, for callers that stop a
// command before its context ends.
func TestKillEndsTheWrappersChild(t *testing.T) {
	t.Parallel()
	wrapper := procgrouptest.WriteWrapper(t, "ffprobe")
	cmd := procgroup.CommandContext(context.Background(), wrapper.Path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := wrapper.ChildPID(t)
	if err := procgroup.Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the killed wrapper exited successfully")
	}
	procgrouptest.RequireExited(t, pid)
}
