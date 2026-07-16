// Package rootcheck probes library root paths for reachability: a root is
// reachable when it exists, is a directory, and can be listed. It is shared
// by the scanner's dead-root protection and the admin mount-check endpoint so
// both agree on what "unreachable" means.
package rootcheck

import (
	"context"
	"errors"
	"os"
	"time"
)

// Error codes reported by Probe. They are part of the admin mount-check API
// response contract.
const (
	ErrCodeNotFound         = "not_found"
	ErrCodePermissionDenied = "permission_denied"
	ErrCodeNotDirectory     = "not_directory"
	ErrCodeReadFailed       = "read_failed"
	ErrCodeStatFailed       = "stat_failed"
	ErrCodeTimeout          = "probe_timeout"
)

// DefaultProbeTimeout bounds how long a single probe may block. A dead mount
// usually errors within milliseconds, but a hung network filesystem
// (hard-mounted NFS, wedged SMB/FUSE) blocks stat/readdir indefinitely —
// probes run on scan and request hot paths, so a hung mount must degrade
// into "unreachable" rather than stall the caller.
const DefaultProbeTimeout = 5 * time.Second

// Result describes the outcome of probing a single root path.
type Result struct {
	Reachable bool
	// Empty is set for a reachable directory with zero entries. A completely
	// empty root is the on-disk signature of a lost mount (the mountpoint
	// directory remains, its contents vanished with the mount), which a
	// reachability check alone cannot detect.
	Empty        bool
	ErrorCode    string // empty when Reachable
	ErrorMessage string // empty when Reachable
}

// Probe checks that path exists, is a directory, and can be listed.
func Probe(path string) Result {
	res := Result{Reachable: true}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		res.Reachable = false
		res.ErrorCode, res.ErrorMessage = classify(err, false)
	case !info.IsDir():
		res.Reachable = false
		res.ErrorCode, res.ErrorMessage = ErrCodeNotDirectory, "Path is not a directory"
	default:
		entries, err := os.ReadDir(path)
		if err != nil {
			res.Reachable = false
			res.ErrorCode, res.ErrorMessage = classify(err, true)
		} else {
			res.Empty = len(entries) == 0
		}
	}
	return res
}

// ProbeWithTimeout runs Probe but gives up once timeout elapses or ctx is
// done, reporting the root unreachable with ErrCodeTimeout. The probing
// goroutine finishes in the background whenever the blocked syscall finally
// returns; its result is discarded.
func ProbeWithTimeout(ctx context.Context, path string, timeout time.Duration) Result {
	return probeBounded(ctx, timeout, func() Result { return Probe(path) })
}

func probeBounded(ctx context.Context, timeout time.Duration, probe func() Result) Result {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	done := make(chan Result, 1)
	go func() { done <- probe() }()

	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		return res
	case <-ctxDone:
	case <-timer.C:
	}
	return Result{
		Reachable:    false,
		ErrorCode:    ErrCodeTimeout,
		ErrorMessage: "Probe timed out; filesystem is not responding",
	}
}

func classify(err error, isRead bool) (string, string) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ErrCodeNotFound, "Path does not exist"
	case errors.Is(err, os.ErrPermission):
		return ErrCodePermissionDenied, "Permission denied"
	case isRead:
		return ErrCodeReadFailed, "Failed to read directory"
	default:
		return ErrCodeStatFailed, "Failed to stat path"
	}
}
