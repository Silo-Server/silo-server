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
	// Darwin wait4 reports ru_maxrss in bytes.
	return float64(usage.Maxrss), true
}
