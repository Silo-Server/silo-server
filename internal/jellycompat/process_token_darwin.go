//go:build darwin

package jellycompat

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func processToken(pid int) string {
	if pid <= 0 {
		return ""
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || process.Proc.P_pid != int32(pid) {
		return ""
	}
	startedAt := process.Proc.P_starttime
	return fmt.Sprintf("%d:%d", startedAt.Sec, startedAt.Usec)
}
