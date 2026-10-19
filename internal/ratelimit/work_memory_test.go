package ratelimit

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func newWorkClock() (*memoryWorkStore, *time.Duration) {
	elapsed := new(time.Duration)
	store := NewMemoryWorkStoreWithClock(time.Unix(1_800_000_000, 125_000_000),
		func() time.Duration { return *elapsed })
	return store, elapsed
}

func requireWork(t *testing.T, got WorkResult, allowed bool, retry time.Duration) {
	t.Helper()
	if got.Unavailable || got.Allowed != allowed || got.RetryAfter != retry {
		t.Fatalf("decision = %+v, want allowed=%t retry=%s", got, allowed, retry)
	}
}

func TestWorkMemoryBothAlignedWindows(t *testing.T) {
	s, elapsed := newWorkClock()
	limit := WorkLimit{PerSecond: 2, PerMinute: 3}
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), false, 875*time.Millisecond)
	*elapsed += 875 * time.Millisecond // Exactly the next aligned second.
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), false, 59*time.Second)
	*elapsed += 59 * time.Second // The minute boundary resets both windows.
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
}

func TestWorkMemoryRetryCoversEveryExhaustedWindow(t *testing.T) {
	s, elapsed := newWorkClock()
	limit := WorkLimit{PerSecond: 1, PerMinute: 1}
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), false, 59_875*time.Millisecond)
	*elapsed += time.Second
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), false, 58_875*time.Millisecond)
	// The refused attempt must not consume a permit in its fresh second window.
	if len(s.entries["scope"]) != 2 {
		t.Fatalf("refusal created counters: %+v", s.entries["scope"])
	}
}

func TestWorkMemoryExpiryStartsAtFirstSuccessfulIncrement(t *testing.T) {
	s, elapsed := newWorkClock()
	limit := WorkLimit{PerSecond: 2, PerMinute: 2}
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	second := workWindow{s.epoch.Truncate(time.Second), time.Second}
	minute := workWindow{s.epoch.Truncate(time.Minute), time.Minute}
	secExpiry, minExpiry := s.entries["scope"][second].expires, s.entries["scope"][minute].expires
	if !secExpiry.Equal(s.epoch.Add(2*time.Second)) || !minExpiry.Equal(s.epoch.Add(2*time.Minute)) {
		t.Fatalf("initial expiries = %v, %v", secExpiry, minExpiry)
	}
	*elapsed += 100 * time.Millisecond
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), false, 59_775*time.Millisecond)
	if !s.entries["scope"][second].expires.Equal(secExpiry) || !s.entries["scope"][minute].expires.Equal(minExpiry) {
		t.Fatal("increment or denial extended logical expiry")
	}
	*elapsed = 2*time.Second - time.Nanosecond
	s.AllowWork(t.Context(), "scope", limit)
	if s.entries["scope"][second].count != 2 {
		t.Fatal("second counter expired early")
	}
	*elapsed += time.Nanosecond
	s.AllowWork(t.Context(), "scope", limit)
	if _, exists := s.entries["scope"][second]; exists {
		t.Fatal("second counter retained at its exact expiry")
	}
	*elapsed = 2*time.Minute - time.Nanosecond
	s.AllowWork(t.Context(), "other", limit) // Sweep all scopes, not just the requested one.
	if s.entries["scope"][minute].count != 2 {
		t.Fatal("minute counter expired early")
	}
	*elapsed += time.Nanosecond
	s.AllowWork(t.Context(), "scope", limit)
	if _, exists := s.entries["scope"][minute]; exists {
		t.Fatal("minute counter retained at its exact expiry")
	}
	*elapsed += 3 * time.Minute
	s.AllowWork(t.Context(), "fresh", limit)
	if len(s.entries) != 1 {
		t.Fatalf("expired scopes not evicted: %d", len(s.entries))
	}
}

func TestWorkMemoryWallRollbackDoesNotFreezeElapsedWindows(t *testing.T) {
	wall := time.Unix(1_800_000_000, 500_000_000)
	elapsed := time.Duration(0)
	s := NewMemoryWorkStoreWithClock(wall, func() time.Duration { return elapsed })
	limit := WorkLimit{PerSecond: 1, PerMinute: 2}
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	wall = wall.Add(-time.Hour) // A separate fake wall axis, never resampled by the store.
	elapsed += time.Second
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	elapsed = 0 // Even a faulty elapsed source cannot revisit a window for fresh credit.
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), false, 58_500*time.Millisecond)
	elapsed = time.Minute
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	if wall.Hour() == time.Unix(1_800_000_000, 500_000_000).Hour() {
		t.Fatal("test wall clock did not roll backwards")
	}
}

func TestWorkMemoryCancellationAndInvalidInputs(t *testing.T) {
	s, _ := newWorkClock()
	limit := WorkLimit{PerSecond: 1, PerMinute: 1}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, decision := range []WorkResult{
		s.AllowWork(ctx, "scope", limit),
		s.AllowWork(t.Context(), "", limit),
		s.AllowWork(t.Context(), "scope", WorkLimit{}),
		s.AllowWork(t.Context(), "scope", WorkLimit{PerSecond: -1, PerMinute: 1}),
	} {
		if decision.Allowed || !decision.Unavailable {
			t.Fatalf("invalid/canceled decision = %+v", decision)
		}
	}
	if len(s.entries) != 0 {
		t.Fatal("invalid/canceled attempts changed counters")
	}
	requireWork(t, s.AllowWork(t.Context(), "scope", limit), true, 0)
	requireWork(t, s.AllowWork(t.Context(), "other", limit), true, 0)
}

func TestWorkMemoryDeadlineWhileWaitingForCounter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newWorkClock()
		s.gate <- struct{}{} // Hold the counter gate without starting any worker.
		defer func() { <-s.gate }()
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()
		start := time.Now()
		got := s.AllowWork(ctx, "synthetic", WorkLimit{1, 1})
		if !got.Unavailable || got.Allowed || time.Since(start) != 250*time.Millisecond || len(s.entries) != 0 {
			t.Fatalf("blocked/canceled counter = %+v elapsed=%s entries=%d", got, time.Since(start), len(s.entries))
		}
	})
}
