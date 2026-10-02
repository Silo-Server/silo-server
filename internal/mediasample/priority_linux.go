//go:build linux

package mediasample

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"sync"

	"golang.org/x/sys/unix"
)

// Background runs start at the lowest CPU priority and in the idle I/O class,
// so analysis only uses CPU and disk time that playback leaves free.
const (
	backgroundNice = 19

	// ioprio_set(2) values; golang.org/x/sys/unix does not define them.
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
)

// backgroundIOPrio is IOPRIO_PRIO_VALUE(IOPRIO_CLASS_IDLE, 0).
const backgroundIOPrio = ioprioClassIdle << ioprioClassShift

var priorityWarning sync.Once

// startBackground starts cmd with background priority.
//
// Linux keeps nice and I/O priority per thread, and a child forked from a
// thread inherits both. So a dedicated goroutine locks itself to a thread,
// lowers that thread's priority, and forks from it. It never unlocks: the Go
// runtime discards a locked thread when its goroutine exits, so no other
// goroutine ever runs at the lowered priority. The runtime also never creates
// new threads from a locked thread, so none can inherit it either.
//
// If the priority cannot be fully lowered, the child starts with whatever
// part of it took effect and the first failure is logged.
func startBackground(cmd *exec.Cmd) error {
	started := make(chan error, 1)
	go startFromDiscardedThread(cmd, started)
	return <-started
}

func startFromDiscardedThread(cmd *exec.Cmd, started chan<- error) {
	runtime.LockOSThread()
	if unix.Gettid() == unix.Getpid() {
		// The runtime never discards the main thread: it parks it for good
		// instead, which would leave the process showing nice 19. Hold the
		// main thread while another goroutine does the work, which keeps that
		// goroutine off it, then release it unchanged.
		inner := make(chan error, 1)
		go startFromDiscardedThread(cmd, inner)
		err := <-inner
		runtime.UnlockOSThread()
		started <- err
		return
	}
	if err := lowerThreadPriority(); err != nil {
		priorityWarning.Do(func() {
			slog.Warn("could not fully lower background media sampling priority", "error", err)
		})
	}
	started <- cmd.Start()
}

// lowerThreadPriority sets the calling thread's nice value and I/O priority.
// It tries both and reports every failure.
func lowerThreadPriority() error {
	var errs []error
	if err := unix.Setpriority(unix.PRIO_PROCESS, 0, backgroundNice); err != nil {
		errs = append(errs, fmt.Errorf("setting nice %d: %w", backgroundNice, err))
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, 0, backgroundIOPrio); errno != 0 {
		errs = append(errs, fmt.Errorf("setting idle I/O priority: %w", errno))
	}
	return errors.Join(errs...)
}
