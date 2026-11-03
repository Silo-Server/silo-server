package bandwidthprobe

import (
	"testing"
	"time"
)

func TestBandwidthProbeLimiterBoundsInFlightProbesPerAccount(t *testing.T) {
	limiter := NewLimiter()
	now := time.Unix(1_000_000, 0)
	for range bandwidthProbeMaxInFlight {
		if ok, _, _ := limiter.Acquire(1, 1000, now); !ok {
			t.Fatal("refused a probe under the limit")
		}
	}
	if ok, _, _ := limiter.Acquire(1, 1000, now); ok {
		t.Fatal("admitted a probe over the limit")
	}
	if ok, _, _ := limiter.Acquire(2, 1000, now); !ok {
		t.Fatal("one account's probes blocked another account")
	}
	limiter.Release(1, 0, now, now)
	if ok, _, _ := limiter.Acquire(1, 1000, now); !ok {
		t.Fatal("a finished probe did not free its slot")
	}
}

func TestBandwidthProbeLimiterBoundsProbesPerNode(t *testing.T) {
	limiter := NewLimiter()
	now := time.Unix(1_000_000, 0)
	for id := range bandwidthProbeNodeMaxInFlight {
		if ok, _, _ := limiter.Acquire(id, 1000, now); !ok {
			t.Fatalf("refused account %d under the node limit", id)
		}
	}
	ok, _, wait := limiter.Acquire(bandwidthProbeNodeMaxInFlight, 1000, now)
	if ok || wait != bandwidthProbeNodeRetryAfter {
		t.Fatalf("admitted=%v wait=%v past the node limit", ok, wait)
	}
	limiter.Release(0, 0, now, now)
	if ok, _, _ := limiter.Acquire(bandwidthProbeNodeMaxInFlight, 1000, now); !ok {
		t.Fatal("a finished probe did not free a node slot")
	}
}

func TestBandwidthProbeLimiterBoundsBytesPerWindow(t *testing.T) {
	limiter := NewLimiter()
	now := time.Unix(1_000_000, 0)
	const size = 100_000_000
	for i := range bandwidthProbeWindowBytes / size {
		ok, window, _ := limiter.Acquire(1, size, now)
		if !ok {
			t.Fatalf("refused probe %d inside the budget", i)
		}
		limiter.Release(1, 0, window, now)
	}
	ok, _, wait := limiter.Acquire(1, size, now.Add(time.Minute))
	if ok {
		t.Fatal("admitted a probe past the byte budget")
	}
	if wait != bandwidthProbeWindow-time.Minute {
		t.Fatalf("retry after %v, want the rest of the window", wait)
	}
	if ok, _, _ := limiter.Acquire(1, size, now.Add(bandwidthProbeWindow)); !ok {
		t.Fatal("a new window did not restore the budget")
	}
}

func TestBandwidthProbeLimiterRefundsUnsentBytes(t *testing.T) {
	limiter := NewLimiter()
	now := time.Unix(1_000_000, 0)
	// One probe more than the window holds unrefunded.
	for range bandwidthProbeWindowBytes/40_000_000 + 1 {
		ok, window, _ := limiter.Acquire(1, 40_000_000, now)
		if !ok {
			t.Fatal("refused a probe the client cut short")
		}
		// The client closed the connection after 2 MB.
		limiter.Release(1, 38_000_000, window, now)
	}
}

func TestBandwidthProbeLimiterRefundsOnlyTheGrantingWindow(t *testing.T) {
	limiter := NewLimiter()
	now := time.Unix(1_000_000, 0)
	_, first, _ := limiter.Acquire(1, 100_000_000, now)
	// The window rolls over while the first probe runs, and the new window
	// fills up.
	later := now.Add(bandwidthProbeWindow)
	for range bandwidthProbeWindowBytes / 100_000_000 {
		if ok, _, _ := limiter.Acquire(1, 100_000_000, later); !ok {
			t.Fatal("refused a probe inside the new window's budget")
		}
		limiter.Release(1, 0, later, later)
	}
	// The first probe ends early; its refund must not reopen the new window.
	limiter.Release(1, 90_000_000, first, later)
	if ok, _, _ := limiter.Acquire(1, 1000, later); ok {
		t.Fatal("a refund from an earlier window reopened the current one")
	}
}

func TestBandwidthProbeLimiterForgetsIdleAccounts(t *testing.T) {
	limiter := NewLimiter()
	now := time.Unix(1_000_000, 0)
	_, window, _ := limiter.Acquire(1, 1000, now)
	limiter.Release(1, 0, window, now.Add(bandwidthProbeWindow))
	if len(limiter.accounts) != 0 {
		t.Fatalf("idle account still tracked: %v", limiter.accounts)
	}

	// A probe that finishes inside its window is swept by a later admission.
	_, window, _ = limiter.Acquire(2, 1000, now)
	limiter.Release(2, 0, window, now)
	limiter.Acquire(3, 1000, now.Add(bandwidthProbeWindow))
	if _, tracked := limiter.accounts[2]; tracked {
		t.Fatal("account whose window passed is still tracked")
	}
	if _, tracked := limiter.accounts[3]; !tracked {
		t.Fatal("the admitted account is not tracked")
	}
}

// An accurate test asks for MaxSize. After the quick tests that came before
// it, it is still admitted; a run of accurate tests is not.
func TestBandwidthProbeLimiterAdmitsAnAccurateTestAfterQuickOnes(t *testing.T) {
	limiter := NewLimiter()
	now := time.Unix(1_000_000, 0)
	for range 3 {
		ok, window, _ := limiter.Acquire(1, MaxSize/10, now)
		if !ok {
			t.Fatal("refused a quick test")
		}
		limiter.Release(1, 0, window, now)
	}
	for i := range 2 {
		ok, window, _ := limiter.Acquire(1, MaxSize, now)
		if (i == 0) != ok {
			t.Fatalf("accurate test %d admitted=%v", i, ok)
		}
		if ok {
			limiter.Release(1, 0, window, now)
		}
	}
}
