package recommendations

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// A count that fails gives equal weights when the scope has no counts yet,
// and the scope is not counted again until titleCountRetry passes.
func TestTitleCountsFallBackToEqualWeightsOnError(t *testing.T) {
	cache := newTitleCountCache()
	var calls atomic.Int32
	failing := func(context.Context, catalog.AccessFilter) ([]int64, error) {
		calls.Add(1)
		return nil, errors.New("db down")
	}
	for range 2 {
		if got := cache.get(t.Context(), catalog.AccessFilter{}, failing); !slices.Equal(got, equalTitleCounts()) {
			t.Fatalf("counts = %v, want equal weights", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("counts run = %d, want 1 until the retry window passes", calls.Load())
	}
}

// Expired counts serve the read at once while a count refreshes them in the
// background; a refresh that fails keeps them.
func TestTitleCountsServeExpiredCountsWhileRefreshing(t *testing.T) {
	cache := newTitleCountCache()
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{3}}
	key := titleCountKey(filter)
	cache.store(key, []int64{5, 2}, -time.Second)

	release := make(chan struct{})
	refreshed := make(chan struct{})
	blocked := func(context.Context, catalog.AccessFilter) ([]int64, error) {
		<-release
		defer close(refreshed)
		return []int64{9, 4}, nil
	}
	if got := cache.get(t.Context(), filter, blocked); !slices.Equal(got, []int64{5, 2}) {
		t.Fatalf("counts = %v, want the expired counts without waiting", got)
	}
	close(release)
	<-refreshed
	waitForCounts(t, cache, key, []int64{9, 4})

	cache.store(key, []int64{9, 4}, -time.Second)
	failed := make(chan struct{})
	failing := func(context.Context, catalog.AccessFilter) ([]int64, error) {
		defer close(failed)
		return nil, errors.New("timeout")
	}
	if got := cache.get(t.Context(), filter, failing); !slices.Equal(got, []int64{9, 4}) {
		t.Fatalf("counts = %v, want the expired counts", got)
	}
	<-failed
	waitForCounts(t, cache, key, []int64{9, 4})
}

// A read waiting on a first count stops waiting when its context ends; the
// count finishes and serves the next read.
func TestTitleCountsStopWaitingWhenTheReadEnds(t *testing.T) {
	cache := newTitleCountCache()
	filter := catalog.AccessFilter{}
	release := make(chan struct{})
	counted := make(chan struct{})
	slow := func(ctx context.Context, _ catalog.AccessFilter) ([]int64, error) {
		<-release
		defer close(counted)
		return []int64{7, 1}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := cache.get(ctx, filter, slow); !slices.Equal(got, equalTitleCounts()) {
		t.Fatalf("counts = %v, want equal weights for a read that ended", got)
	}
	close(release)
	<-counted
	waitForCounts(t, cache, titleCountKey(filter), []int64{7, 1})
}

// waitForCounts waits until the background count stores want for key.
func waitForCounts(t *testing.T, cache *titleCountCache, key [32]byte, want []int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		counts, _, _ := cache.lookup(key)
		if slices.Equal(counts, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored counts = %v, want %v", counts, want)
		}
		time.Sleep(time.Millisecond)
	}
}
