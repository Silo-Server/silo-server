package logthrottle

import (
	"testing"
	"time"
)

func TestKeyedAllowsOnePerKeyPerIntervalAndCountsTheRest(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	throttle := New[string](time.Minute)
	throttle.Now = func() time.Time { return clock }

	if suppressed, ok := throttle.Allow("a"); !ok || suppressed != 0 {
		t.Fatalf("first warning = %d %v, want allowed", suppressed, ok)
	}
	for range 3 {
		if _, ok := throttle.Allow("a"); ok {
			t.Fatal("a repeat inside the interval was allowed")
		}
	}
	if _, ok := throttle.Allow("b"); !ok {
		t.Fatal("another key was held back by the first")
	}
	clock = clock.Add(time.Minute)
	if suppressed, ok := throttle.Allow("a"); !ok || suppressed != 3 {
		t.Fatalf("after the interval = %d %v, want allowed with 3 held back", suppressed, ok)
	}
	if _, ok := throttle.Allow("a"); ok {
		t.Fatal("the interval did not restart")
	}
}

func TestKeyedStaysBoundedUnderAFloodOfKeys(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	throttle := New[int](time.Minute)
	throttle.Now = func() time.Time { return clock }
	for key := range maxKeys {
		if _, ok := throttle.Allow(key); !ok {
			t.Fatalf("key %d held back below the bound", key)
		}
	}
	if _, ok := throttle.Allow(maxKeys); ok {
		t.Fatal("a key past the bound was allowed while every tracked key is inside its interval")
	}
	if len(throttle.entries) != maxKeys {
		t.Fatalf("tracking %d keys, want %d", len(throttle.entries), maxKeys)
	}
	clock = clock.Add(time.Minute)
	if _, ok := throttle.Allow(maxKeys); !ok {
		t.Fatal("a new key was held back after the tracked ones expired")
	}
	if len(throttle.entries) != 1 {
		t.Fatalf("tracking %d keys after pruning, want 1", len(throttle.entries))
	}
}
