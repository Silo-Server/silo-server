package processmetrics

import (
	"os"
	"syscall"
)

func maximumRSS(state *os.ProcessState) (float64, bool) {
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage.Maxrss < 0 {
		return 0, false
	}
	// Linux wait4 reports ru_maxrss in KiB.
	return float64(usage.Maxrss) * 1024, true
}
