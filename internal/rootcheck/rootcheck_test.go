package rootcheck

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeReachableDirectory(t *testing.T) {
	t.Parallel()

	res := Probe(t.TempDir())
	if !res.Reachable {
		t.Fatalf("Probe(temp dir) = %+v, want reachable", res)
	}
	if res.ErrorCode != "" || res.ErrorMessage != "" {
		t.Fatalf("Probe(temp dir) error fields = %q/%q, want empty", res.ErrorCode, res.ErrorMessage)
	}
}

func TestProbeMissingPath(t *testing.T) {
	t.Parallel()

	res := Probe(filepath.Join(t.TempDir(), "does-not-exist"))
	if res.Reachable {
		t.Fatal("Probe(missing path) reported reachable")
	}
	if res.ErrorCode != ErrCodeNotFound {
		t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrCodeNotFound)
	}
}

func TestProbeRegularFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	res := Probe(path)
	if res.Reachable {
		t.Fatal("Probe(regular file) reported reachable")
	}
	if res.ErrorCode != ErrCodeNotDirectory {
		t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrCodeNotDirectory)
	}
}

func TestProbeUnreadableDirectory(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}

	path := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(path, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })

	res := Probe(path)
	if res.Reachable {
		t.Fatal("Probe(unreadable dir) reported reachable")
	}
	if res.ErrorCode != ErrCodePermissionDenied {
		t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrCodePermissionDenied)
	}
}

func TestProbeWithTimeoutReachableDirectory(t *testing.T) {
	t.Parallel()

	res := ProbeWithTimeout(context.Background(), t.TempDir(), DefaultProbeTimeout)
	if !res.Reachable {
		t.Fatalf("ProbeWithTimeout(temp dir) = %+v, want reachable", res)
	}
}

func TestProbeBoundedTimesOutOnHungProbe(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	res := probeBounded(context.Background(), 10*time.Millisecond, func() Result {
		<-release // simulate a stat/readdir blocked on a hung mount
		return Result{Reachable: true}
	})
	if res.Reachable {
		t.Fatal("hung probe reported reachable")
	}
	if res.ErrorCode != ErrCodeTimeout {
		t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrCodeTimeout)
	}
}

func TestProbeBoundedHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	res := probeBounded(ctx, time.Minute, func() Result {
		<-release
		return Result{Reachable: true}
	})
	if res.Reachable || res.ErrorCode != ErrCodeTimeout {
		t.Fatalf("canceled probe = %+v, want unreachable/%s", res, ErrCodeTimeout)
	}
}

func TestProbeBoundedReturnsFastResult(t *testing.T) {
	t.Parallel()

	res := probeBounded(context.Background(), time.Minute, func() Result {
		return Result{Reachable: false, ErrorCode: ErrCodeNotFound, ErrorMessage: "Path does not exist"}
	})
	if res.Reachable || res.ErrorCode != ErrCodeNotFound {
		t.Fatalf("probeBounded passthrough = %+v, want the probe's own result", res)
	}
}

func TestProbeCoordinatorCoalescesBlockedPath(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	coordinator := newProbeCoordinator()
	probe := func(string) Result {
		calls.Add(1)
		<-release
		return Result{Reachable: true}
	}

	results := make(chan Result, 2)
	for range 2 {
		go func() {
			results <- coordinator.probe(context.Background(), "/hung", 20*time.Millisecond, probe)
		}()
	}
	for range 2 {
		if result := <-results; result.Reachable || result.ErrorCode != ErrCodeTimeout {
			t.Fatalf("coalesced blocked probe = %+v, want timeout", result)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("underlying probe calls = %d, want 1", got)
	}
}

func TestProbeManyPreservesOrderAndBoundsConcurrency(t *testing.T) {
	t.Parallel()

	var active atomic.Int32
	var peak atomic.Int32
	probe := func(_ context.Context, path string, _ time.Duration) Result {
		current := active.Add(1)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return Result{Reachable: true, ErrorMessage: path}
	}

	paths := []string{"one", "two", "three", "four"}
	results := probeMany(context.Background(), paths, time.Second, 2, probe)
	if got := peak.Load(); got > 2 {
		t.Fatalf("peak concurrent probes = %d, want at most 2", got)
	}
	for i, result := range results {
		if result.ErrorMessage != paths[i] {
			t.Fatalf("result[%d] = %q, want %q", i, result.ErrorMessage, paths[i])
		}
	}
}

func TestProbeReportsEmptyDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	res := Probe(dir)
	if !res.Reachable || !res.Empty {
		t.Fatalf("Probe(empty dir) = %+v, want reachable and empty", res)
	}

	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	res = Probe(dir)
	if !res.Reachable || res.Empty {
		t.Fatalf("Probe(non-empty dir) = %+v, want reachable and not empty", res)
	}
}
