package requests

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLogoLookup struct {
	calls   atomic.Int64
	logoFor map[int]string
	err     error
}

func (f *fakeLogoLookup) Lookup(_ context.Context, id int) (string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return "", f.err
	}
	return f.logoFor[id], nil
}

func TestLogoCacheReturnsCachedValueAfterFirstCall(t *testing.T) {
	lookup := &fakeLogoLookup{logoFor: map[int]string{420: "/hUze.png"}}
	cache := newLogoCache(lookup.Lookup, time.Hour)

	for i := 0; i < 3; i++ {
		got, err := cache.Get(context.Background(), 420)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "/hUze.png" {
			t.Errorf("got %q, want /hUze.png", got)
		}
	}
	if calls := lookup.calls.Load(); calls != 1 {
		t.Errorf("upstream calls = %d, want 1", calls)
	}
}

func TestLogoCacheExpiresAfterTTL(t *testing.T) {
	lookup := &fakeLogoLookup{logoFor: map[int]string{1: "/a.png"}}
	cache := newLogoCache(lookup.Lookup, 10*time.Millisecond)

	if _, err := cache.Get(context.Background(), 1); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := cache.Get(context.Background(), 1); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if calls := lookup.calls.Load(); calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestLogoCacheSingleflightDeduplicatesParallelMisses(t *testing.T) {
	lookup := &fakeLogoLookup{logoFor: map[int]string{1: "/a.png"}}
	cache := newLogoCache(func(ctx context.Context, id int) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return lookup.Lookup(ctx, id)
	}, time.Hour)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Get(context.Background(), 1); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls := lookup.calls.Load(); calls != 1 {
		t.Errorf("calls = %d, want 1 (singleflight should dedupe)", calls)
	}
}

func TestLogoCacheReturnsErrorAndDoesNotCacheFailure(t *testing.T) {
	failure := errors.New("upstream boom")
	lookup := &fakeLogoLookup{err: failure}
	cache := newLogoCache(lookup.Lookup, time.Hour)

	if _, err := cache.Get(context.Background(), 1); !errors.Is(err, failure) {
		t.Fatalf("first err = %v, want %v", err, failure)
	}
	if _, err := cache.Get(context.Background(), 1); !errors.Is(err, failure) {
		t.Fatalf("second err = %v, want %v", err, failure)
	}
	if calls := lookup.calls.Load(); calls != 2 {
		t.Errorf("calls = %d, want 2 (errors must not be cached)", calls)
	}
}

func TestLogoCacheEmptyPathIsCachedAsMiss(t *testing.T) {
	lookup := &fakeLogoLookup{logoFor: map[int]string{1: ""}}
	cache := newLogoCache(lookup.Lookup, time.Hour)

	for i := 0; i < 3; i++ {
		got, err := cache.Get(context.Background(), 1)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	}
	if calls := lookup.calls.Load(); calls != 1 {
		t.Errorf("calls = %d, want 1 (empty strings should still be cached)", calls)
	}
}
