//go:build linux

package embeddedsub

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// ioprio_set(2) values; golang.org/x/sys/unix does not define them.
const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
)

// idleIOThread locks the calling goroutine to its thread and puts the
// thread in the idle I/O class. Linux keeps I/O priority per thread, and
// the caller never unlocks: the runtime discards a locked thread when its
// goroutine exits, so no other goroutine runs at the lowered priority.
//
// The runtime never discards the main thread. On it, idleIOThread reports
// false and leaves the goroutine locked there, so the caller can start its
// replacement, which can't run on the main thread meanwhile, before it
// unlocks.
func idleIOThread() bool {
	runtime.LockOSThread()
	if unix.Gettid() == unix.Getpid() {
		return false
	}
	// A failure leaves the thread's reads at normal priority.
	_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, 0, ioprioClassIdle<<ioprioClassShift)
	return true
}
