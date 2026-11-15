package logthrottle

import (
	"testing"
	"time"
)

// A full throttle searches for expired keys at most once per interval: a
// flood of new keys must not cost a pass over the whole map each.
func TestAFullThrottleSearchesOncePerInterval(t *testing.T) {
	const interval = time.Minute
	now := time.Unix(1_700_000_000, 0)
	throttle := New[int](interval)
	throttle.SetClock(func() time.Time { return now })

	for key := range maxKeys {
		if !throttle.Allow(key) {
			t.Fatalf("key %d refused while the throttle had room", key)
		}
	}
	now = now.Add(interval / 2)
	if throttle.Allow(maxKeys) {
		t.Fatal("a new key was let through a full throttle of recent keys")
	}
	// The first keys have expired, but the last search was half an interval
	// ago: no second search yet.
	now = now.Add(interval / 2)
	if throttle.Allow(maxKeys) {
		t.Fatal("the full throttle was searched again within the interval")
	}
	now = now.Add(interval / 2)
	if !throttle.Allow(maxKeys) {
		t.Fatal("a new key was refused after the expired keys could be dropped")
	}
}
