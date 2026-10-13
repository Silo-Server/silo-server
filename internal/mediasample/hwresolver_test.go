package mediasample

import (
	"context"
	"testing"
	"time"
)

// fakeResolve resolves every configured accelerator to resolved and counts
// its calls.
func fakeResolve(resolved string, calls *int) func(context.Context, string, string, string) string {
	return func(_ context.Context, hwAccel, _, _ string) string {
		*calls++
		if hwAccel == "auto" {
			return resolved
		}
		return hwAccel
	}
}

func TestHardwareResolverDoesNotCacheACanceledProbe(t *testing.T) {
	resolver := NewHardwareResolver("auto", "")
	var calls int
	resolver.Resolve = fakeResolve("none", &calls)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	resolver.Backend(canceled, "ffmpeg")
	resolver.Resolve = fakeResolve("qsv", &calls)
	if accel, _ := resolver.Backend(t.Context(), "ffmpeg"); accel != "qsv" || calls != 2 {
		t.Fatalf("backend %q after %d probes, want qsv after 2", accel, calls)
	}
	if accel, _ := resolver.Backend(t.Context(), "ffmpeg"); accel != "qsv" || calls != 2 {
		t.Fatalf("backend %q after %d probes, want the cached qsv", accel, calls)
	}
}

// TestHardwareResolverRetriesAFailedAutoDetection resolves "auto" to no
// hardware, as a smoke probe that failed under GPU contention does, and
// expects the resolver to ask again once HardwareRetryInterval has passed or
// the playback probe cache was invalidated, while a found backend stays
// cached until an invalidation.
func TestHardwareResolverRetriesAFailedAutoDetection(t *testing.T) {
	resolver := NewHardwareResolver("auto", "")
	now := time.Unix(1_000_000, 0)
	var generation uint64
	resolver.Now = func() time.Time { return now }
	resolver.Generation = func() uint64 { return generation }
	var calls int
	resolver.Resolve = fakeResolve("none", &calls)
	backend := func(want string, wantCalls int) {
		t.Helper()
		if accel, _ := resolver.Backend(t.Context(), "ffmpeg"); accel != want || calls != wantCalls {
			t.Fatalf("backend %q after %d resolutions, want %q after %d", accel, calls, want, wantCalls)
		}
	}
	backend("none", 1)
	now = now.Add(HardwareRetryInterval - time.Second)
	backend("none", 1)

	// The GPU recovers: the next resolution after the interval finds it.
	resolver.Resolve = fakeResolve("vaapi", &calls)
	now = now.Add(time.Second)
	backend("vaapi", 2)
	now = now.Add(24 * time.Hour)
	backend("vaapi", 2)

	// An operator's re-probe invalidates a found backend too.
	resolver.Resolve = fakeResolve("none", &calls)
	generation++
	backend("none", 3)
	resolver.Resolve = fakeResolve("qsv", &calls)
	generation++
	backend("qsv", 4)
}

func TestHardwareResolverReportsEachBackendsFirstFailure(t *testing.T) {
	h := NewHardwareResolver("auto", "")
	if !h.FirstFailure("vaapi") || h.FirstFailure("vaapi") || !h.FirstFailure("qsv") {
		t.Fatal("want one first failure per accelerator")
	}
}
