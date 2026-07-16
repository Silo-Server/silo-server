// Package rootcheck probes library root paths for reachability: a root is
// reachable when it exists, is a directory, and can be listed. It is shared
// by the scanner's dead-root protection and the admin mount-check endpoint so
// both agree on what "unreachable" means.
package rootcheck

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
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

// DefaultProbeConcurrency bounds the number of distinct roots started by one
// batch. Repeated probes for the same path are also coalesced process-wide.
const DefaultProbeConcurrency = 8

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
		dir, err := os.Open(path)
		if err == nil {
			_, err = dir.Readdirnames(1)
			_ = dir.Close()
		}
		if errors.Is(err, io.EOF) {
			res.Empty = true
		} else if err != nil {
			res.Reachable = false
			res.ErrorCode, res.ErrorMessage = classify(err, true)
		}
	}
	return res
}

type probeCall struct {
	done   chan struct{}
	result Result
}

type probeCoordinator struct {
	mu       sync.Mutex
	inFlight map[string]*probeCall
}

func newProbeCoordinator() *probeCoordinator {
	return &probeCoordinator{inFlight: make(map[string]*probeCall)}
}

var sharedProbes = newProbeCoordinator()

// ProbeWithTimeout runs Probe but gives up once timeout elapses or ctx is
// done, reporting the root unreachable with ErrCodeTimeout. Concurrent calls
// for the same path share one underlying syscall so a wedged mount cannot
// accumulate one blocked goroutine per scan or mount check.
func ProbeWithTimeout(ctx context.Context, path string, timeout time.Duration) Result {
	return sharedProbes.probe(ctx, path, timeout, func(path string) Result { return Probe(path) })
}

func (c *probeCoordinator) probe(
	ctx context.Context,
	path string,
	timeout time.Duration,
	probe func(string) Result,
) Result {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	c.mu.Lock()
	call := c.inFlight[path]
	if call == nil {
		call = &probeCall{done: make(chan struct{})}
		c.inFlight[path] = call
		go func() {
			call.result = probe(path)
			close(call.done)
			c.mu.Lock()
			delete(c.inFlight, path)
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()

	return awaitProbe(ctx, timeout, call.done, func() Result { return call.result })
}

// ProbeManyWithTimeout probes paths concurrently while preserving input order.
func ProbeManyWithTimeout(ctx context.Context, paths []string, timeout time.Duration) []Result {
	return probeMany(ctx, paths, timeout, DefaultProbeConcurrency, ProbeWithTimeout)
}

func probeMany(
	ctx context.Context,
	paths []string,
	timeout time.Duration,
	limit int,
	probe func(context.Context, string, time.Duration) Result,
) []Result {
	results := make([]Result, len(paths))
	if len(paths) == 0 {
		return results
	}
	if limit <= 0 || limit > len(paths) {
		limit = len(paths)
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(limit)
	for range limit {
		go func() {
			defer workers.Done()
			for i := range jobs {
				results[i] = probe(ctx, paths[i], timeout)
			}
		}()
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	return results
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
	return timeoutResult()
}

func awaitProbe(ctx context.Context, timeout time.Duration, done <-chan struct{}, result func() Result) Result {
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return result()
	case <-ctxDone:
	case <-timer.C:
	}
	return timeoutResult()
}

func timeoutResult() Result {
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
