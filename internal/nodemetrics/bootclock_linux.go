//go:build linux

package nodemetrics

import (
	"time"

	"golang.org/x/sys/unix"
)

// bootClockTicks reads the kernel's boot clock, the clock /proc/PID/stat start
// times count on, in USER_HZ ticks.
func bootClockTicks() (uint64, bool) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0, false
	}
	ns := ts.Nano()
	if ns < 0 {
		return 0, false
	}
	return uint64(ns) / uint64(time.Second/procfsUserHZ), true
}
