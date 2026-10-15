package apiv2

import (
	"net/http"
	"testing"
	"time"
)

func TestBandwidthProbeLimiterBoundsInFlightProbesPerAccount(t *testing.T) {
	limiter := &bandwidthProbeLimiter{accounts: map[int]*bandwidthProbeAccount{}}
	now := time.Unix(1_000_000, 0)
	for range bandwidthProbeMaxInFlight {
		if ok, _ := limiter.acquire(1, 1000, now); !ok {
			t.Fatal("refused a probe under the limit")
		}
	}
	if ok, _ := limiter.acquire(1, 1000, now); ok {
		t.Fatal("admitted a probe over the limit")
	}
	if ok, _ := limiter.acquire(2, 1000, now); !ok {
		t.Fatal("one account's probes blocked another account")
	}
	limiter.release(1, 0, now)
	if ok, _ := limiter.acquire(1, 1000, now); !ok {
		t.Fatal("a finished probe did not free its slot")
	}
}

func TestBandwidthProbeLimiterBoundsBytesPerWindow(t *testing.T) {
	limiter := &bandwidthProbeLimiter{accounts: map[int]*bandwidthProbeAccount{}}
	now := time.Unix(1_000_000, 0)
	const size = 100_000_000
	for i := range bandwidthProbeWindowBytes / size {
		ok, _ := limiter.acquire(1, size, now)
		if !ok {
			t.Fatalf("refused probe %d inside the budget", i)
		}
		limiter.release(1, 0, now)
	}
	ok, wait := limiter.acquire(1, size, now.Add(time.Minute))
	if ok {
		t.Fatal("admitted a probe past the byte budget")
	}
	if wait != bandwidthProbeWindow-time.Minute {
		t.Fatalf("retry after %v, want the rest of the window", wait)
	}
	if ok, _ := limiter.acquire(1, size, now.Add(bandwidthProbeWindow)); !ok {
		t.Fatal("a new window did not restore the budget")
	}
}

func TestBandwidthProbeLimiterRefundsUnsentBytes(t *testing.T) {
	limiter := &bandwidthProbeLimiter{accounts: map[int]*bandwidthProbeAccount{}}
	now := time.Unix(1_000_000, 0)
	for range 50 {
		ok, _ := limiter.acquire(1, 40_000_000, now)
		if !ok {
			t.Fatal("refused a probe the client cut short")
		}
		// The client closed the connection after 2 MB.
		limiter.release(1, 38_000_000, now)
	}
}

func TestBandwidthProbeLimiterForgetsIdleAccounts(t *testing.T) {
	limiter := &bandwidthProbeLimiter{accounts: map[int]*bandwidthProbeAccount{}}
	now := time.Unix(1_000_000, 0)
	limiter.acquire(1, 1000, now)
	limiter.release(1, 0, now.Add(bandwidthProbeWindow))
	if len(limiter.accounts) != 0 {
		t.Fatalf("idle account still tracked: %v", limiter.accounts)
	}

	// A probe that finishes inside its window is swept by a later admission.
	limiter.acquire(2, 1000, now)
	limiter.release(2, 0, now)
	limiter.acquire(3, 1000, now.Add(bandwidthProbeWindow))
	if _, tracked := limiter.accounts[2]; tracked {
		t.Fatal("account whose window passed is still tracked")
	}
	if _, tracked := limiter.accounts[3]; !tracked {
		t.Fatal("the admitted account is not tracked")
	}
}

func TestPlaybackBandwidthProbe(t *testing.T) {
	handler := NewHandler(parityDeps(false))
	path := Prefix + "/playback/bandwidth-probe"

	t.Run("requires authentication", func(t *testing.T) {
		response := do(t, handler, http.MethodGet, path, "", nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", response.Code)
		}
	})

	t.Run("sends the requested byte count uncached", func(t *testing.T) {
		response := do(t, handler, http.MethodGet, path+"?size=70000", "", bearer(memberToken))
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		if got := response.Body.Len(); got != 70000 {
			t.Fatalf("body %d bytes, want 70000", got)
		}
		if got := response.Header().Get("Content-Length"); got != "70000" {
			t.Fatalf("Content-Length %q", got)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control %q, want no-store", got)
		}
	})

	t.Run("defaults the size", func(t *testing.T) {
		response := do(t, handler, http.MethodGet, path, "", bearer(memberToken))
		if response.Code != http.StatusOK || response.Body.Len() != bandwidthProbeDefaultSize {
			t.Fatalf("status %d, %d bytes", response.Code, response.Body.Len())
		}
	})

	for _, size := range []string{"0", "-1", "abc", "100000001"} {
		t.Run("rejects size "+size, func(t *testing.T) {
			requireProblem(t, do(t, handler, http.MethodGet, path+"?size="+size, "", bearer(memberToken)), TypeValidationFailed)
		})
	}
}
