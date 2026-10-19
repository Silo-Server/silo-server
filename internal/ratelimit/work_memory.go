package ratelimit

import (
	"context"
	"maps"
	"time"
)

type workWindow struct {
	start time.Time
	width time.Duration
}

type workCounter struct {
	count   int
	expires time.Time
}

type memoryWorkStore struct {
	gate        chan struct{}
	epoch       time.Time
	elapsed     func() time.Duration
	lastElapsed time.Duration
	nextSweep   time.Time
	entries     map[string]map[workWindow]workCounter
}

// NewMemoryWorkStore creates process-local fixed-window counters. Advancing
// monotonic elapsed time from one wall-clock anchor preserves initial alignment
// without freezing windows when the wall clock moves backwards.
func NewMemoryWorkStore() WorkStore { //nolint:iface // The public constructor contract returns WorkStore.
	start := time.Now()
	return NewMemoryWorkStoreWithClock(start, func() time.Duration { return time.Since(start) })
}

// NewMemoryWorkStoreWithClock supplies an initial wall epoch and monotonic
// elapsed time for finite tests. Fresh wall-clock samples must not drive elapsed.
func NewMemoryWorkStoreWithClock(epoch time.Time, elapsed func() time.Duration) *memoryWorkStore {
	return &memoryWorkStore{
		gate:  make(chan struct{}, 1),
		epoch: epoch.Round(0).UTC(), elapsed: elapsed,
		entries: make(map[string]map[workWindow]workCounter),
	}
}

func (s *memoryWorkStore) AllowWork(ctx context.Context, key string, limit WorkLimit) WorkResult {
	if ctx.Err() != nil || key == "" || !validWorkLimit(limit) || s.elapsed == nil {
		return workUnavailable()
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return workUnavailable()
	}
	if ctx.Err() != nil {
		return workUnavailable()
	}
	s.lastElapsed = max(s.lastElapsed, s.elapsed())
	now := s.epoch.Add(s.lastElapsed)
	s.sweep(ctx, now)
	windows := s.entries[key]
	pruneWorkWindows(windows, now)
	second := workWindow{start: now.Truncate(time.Second), width: time.Second}
	minute := workWindow{start: now.Truncate(time.Minute), width: time.Minute}
	var retry time.Duration
	if windows[second].count >= limit.PerSecond {
		retry = second.start.Add(second.width).Sub(now)
	}
	if windows[minute].count >= limit.PerMinute {
		retry = max(retry, minute.start.Add(minute.width).Sub(now))
	}
	if ctx.Err() != nil {
		return workUnavailable()
	}
	if retry > 0 {
		return WorkResult{RetryAfter: retry}
	}
	if windows == nil {
		windows = make(map[workWindow]workCounter)
		s.entries[key] = windows
	}
	incrementWorkWindow(windows, second, now)
	incrementWorkWindow(windows, minute, now)
	if ctx.Err() != nil {
		return workUnavailable()
	}
	return WorkResult{Allowed: true}
}

func incrementWorkWindow(windows map[workWindow]workCounter, window workWindow, now time.Time) {
	counter := windows[window]
	if counter.count == 0 {
		counter.expires = now.Add(2 * window.width)
	}
	counter.count++
	windows[window] = counter
}

func pruneWorkWindows(windows map[workWindow]workCounter, now time.Time) {
	maps.DeleteFunc(windows, func(_ workWindow, counter workCounter) bool {
		return !now.Before(counter.expires)
	})
}

// Opportunistic cleanup needs no goroutine or separate lifecycle. Cancellation
// stops a sweep; the next call resumes it before advancing its schedule.
func (s *memoryWorkStore) sweep(ctx context.Context, now time.Time) {
	if now.Before(s.nextSweep) {
		return
	}
	for key, windows := range s.entries {
		if ctx.Err() != nil {
			return
		}
		pruneWorkWindows(windows, now)
		if len(windows) == 0 {
			delete(s.entries, key)
		}
	}
	s.nextSweep = now.Add(time.Minute)
}
