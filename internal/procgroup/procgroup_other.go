//go:build !unix

package procgroup

import (
	"os/exec"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

// Kill kills a started command from CommandContext, before Wait returns.
// Without Unix process groups, a child it started keeps running.
func Kill(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
