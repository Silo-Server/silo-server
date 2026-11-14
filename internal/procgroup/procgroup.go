// Package procgroup starts external media tools, such as ffmpeg and ffprobe,
// so that stopping one ends every process it started.
//
// The configured ffmpeg or ffprobe path may be a wrapper script that runs the
// real binary as a child instead of exec-ing it. exec.CommandContext kills
// only the process it started, so a deadline or a stopped stream would kill
// the wrapper and leave the real binary running, often still stuck on a
// storage read and outside every concurrency limit. On Unix a command from
// this package runs in a process group of its own, and the end of its context
// kills the whole group. Elsewhere it keeps exec.CommandContext's behavior.
package procgroup

import (
	"context"
	"os/exec"
	"time"
)

// DefaultWaitDelay bounds how long Wait waits for a stopped command's pipes
// to close. A child that left the group can keep them open; without a bound
// Wait would wait for it.
const DefaultWaitDelay = 5 * time.Second

// CommandContext is exec.CommandContext for a command whose end, when ctx
// ends, kills its whole process tree. WaitDelay is DefaultWaitDelay; callers
// may change it. Callers must not replace SysProcAttr or Cancel.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: callers pass the configured ffmpeg or ffprobe path.
	cmd.SysProcAttr = sysProcAttr()
	cmd.Cancel = func() error { return Kill(cmd) }
	cmd.WaitDelay = DefaultWaitDelay
	return cmd
}
