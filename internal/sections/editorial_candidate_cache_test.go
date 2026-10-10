package sections

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestCachedEditorialCandidatesReusesCandidateListForSameScope(t *testing.T) {
	t.Parallel()

	f := &Fetcher{
		Clock: fixedClock(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)),
	}
	calls := 0
	loader := func(context.Context, string, *int, []int, catalog.AccessFilter) ([]string, error) {
		calls++
		return []string{"first", "second"}, nil
	}

	first, err := f.cachedEditorialCandidates(context.Background(), "actor", nil, []int{2, 1}, catalog.AccessFilter{
		MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"},
	}, time.Hour, loader)
	if err != nil {
		t.Fatalf("first cachedEditorialCandidates: %v", err)
	}
	first[0] = "mutated"

	second, err := f.cachedEditorialCandidates(context.Background(), "actor", nil, []int{1, 2}, catalog.AccessFilter{
		MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"},
	}, time.Hour, loader)
	if err != nil {
		t.Fatalf("second cachedEditorialCandidates: %v", err)
	}

	if calls != 1 {
		t.Fatalf("loader calls = %d, want 1", calls)
	}
	if got, want := second[0], "first"; got != want {
		t.Fatalf("cached candidates were mutated through returned slice: got %q, want %q", got, want)
	}
}

func TestCachedEditorialCandidatesSeparatesAccessScopeAndExpires(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	f := &Fetcher{
		Clock: fixedClock(now),
	}
	calls := 0
	loader := func(context.Context, string, *int, []int, catalog.AccessFilter) ([]string, error) {
		calls++
		return []string{time.Unix(int64(calls), 0).UTC().Format(time.RFC3339)}, nil
	}

	filter := catalog.AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}
	if _, err := f.cachedEditorialCandidates(context.Background(), "actor", nil, nil, filter, time.Hour, loader); err != nil {
		t.Fatalf("first cachedEditorialCandidates: %v", err)
	}
	if _, err := f.cachedEditorialCandidates(context.Background(), "actor", nil, nil, catalog.AccessFilter{
		MaturityLimits: access.MaturityLimits{MaxContentRating: "R"},
	}, time.Hour, loader); err != nil {
		t.Fatalf("different filter cachedEditorialCandidates: %v", err)
	}

	f.Clock = fixedClock(now.Add(2 * time.Hour))
	if _, err := f.cachedEditorialCandidates(context.Background(), "actor", nil, nil, filter, time.Hour, loader); err != nil {
		t.Fatalf("expired cachedEditorialCandidates: %v", err)
	}

	if calls != 3 {
		t.Fatalf("loader calls = %d, want 3", calls)
	}
}

func TestCachedEditorialCandidatesSeparatesNilAndEmptyLibraryScope(t *testing.T) {
	t.Parallel()

	f := &Fetcher{
		Clock: fixedClock(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)),
	}
	calls := 0
	loader := func(context.Context, string, *int, []int, catalog.AccessFilter) ([]string, error) {
		calls++
		return []string{"ok"}, nil
	}

	if _, err := f.cachedEditorialCandidates(context.Background(), "actor", nil, nil, catalog.AccessFilter{}, time.Hour, loader); err != nil {
		t.Fatalf("nil scope cachedEditorialCandidates: %v", err)
	}
	if _, err := f.cachedEditorialCandidates(context.Background(), "actor", nil, []int{}, catalog.AccessFilter{}, time.Hour, loader); err != nil {
		t.Fatalf("empty scope cachedEditorialCandidates: %v", err)
	}

	if calls != 2 {
		t.Fatalf("loader calls = %d, want 2", calls)
	}
}

func TestCachedEditorialCandidatesCoalescesConcurrentMisses(t *testing.T) {
	t.Parallel()

	f := &Fetcher{
		Clock: fixedClock(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)),
	}
	var (
		mu    sync.Mutex
		calls int
	)
	started := make(chan struct{})
	release := make(chan struct{})
	loader := func(context.Context, string, *int, []int, catalog.AccessFilter) ([]string, error) {
		mu.Lock()
		calls++
		if calls == 1 {
			close(started)
		}
		mu.Unlock()
		<-release
		return []string{"shared"}, nil
	}

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	errs := make(chan error, workers)
	for range workers {
		go func() {
			defer wg.Done()
			_, err := f.cachedEditorialCandidates(context.Background(), "actor", nil, nil, catalog.AccessFilter{}, time.Hour, loader)
			errs <- err
		}()
	}

	<-started
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("cachedEditorialCandidates: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("loader calls = %d, want 1", calls)
	}
}

// Requests for the same scope share one load. The request that started it
// leaving must not fail the load for the others.
func TestCachedEditorialCandidatesFollowerSurvivesLeaderCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &Fetcher{}
		release := make(chan struct{})
		var calls int
		loader := func(ctx context.Context, _ string, _ *int, _ []int, _ catalog.AccessFilter) ([]string, error) {
			calls++
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return []string{"first"}, nil
			}
		}

		leaderCtx, cancelLeader := context.WithCancel(t.Context())
		leaderDone := make(chan struct{})
		go func() {
			defer close(leaderDone)
			_, _ = f.cachedEditorialCandidates(leaderCtx, "actor", nil, nil, catalog.AccessFilter{}, time.Hour, loader)
		}()
		synctest.Wait() // the leader's load is running
		var (
			followed []string
			err      error
		)
		followerDone := make(chan struct{})
		go func() {
			defer close(followerDone)
			followed, err = f.cachedEditorialCandidates(t.Context(), "actor", nil, nil, catalog.AccessFilter{}, time.Hour, loader)
		}()
		synctest.Wait() // the follower is waiting on the same load
		cancelLeader()
		<-leaderDone // the leader stops waiting without ending the load
		close(release)
		<-followerDone

		if err != nil || len(followed) != 1 || followed[0] != "first" {
			t.Fatalf("follower candidates = %v, err = %v after the leader left", followed, err)
		}
		if calls != 1 {
			t.Fatalf("loader calls = %d, want 1 shared load", calls)
		}
	})
}

// The shared load runs on its own goroutine, where a panic would end the
// process instead of reaching the request's recovery.
func TestCachedEditorialCandidatesSurvivesLoaderPanic(t *testing.T) {
	f := &Fetcher{}
	loader := func(context.Context, string, *int, []int, catalog.AccessFilter) ([]string, error) {
		panic("loader bug")
	}
	if candidates, err := f.cachedEditorialCandidates(t.Context(), "actor", nil, nil, catalog.AccessFilter{}, time.Hour, loader); err == nil || len(candidates) != 0 {
		t.Fatalf("panicking loader returned %v, %v", candidates, err)
	}
}

// A request that has already ended must not start a load nobody waits on.
func TestCachedEditorialCandidatesCanceledCallerStartsNoLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &Fetcher{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var calls int
		loader := func(context.Context, string, *int, []int, catalog.AccessFilter) ([]string, error) {
			calls++
			return []string{"first"}, nil
		}
		if _, err := f.cachedEditorialCandidates(ctx, "actor", nil, nil, catalog.AccessFilter{}, time.Hour, loader); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		synctest.Wait() // any load the call started has finished
		if calls != 0 {
			t.Fatalf("loader calls = %d for a canceled caller, want 0", calls)
		}
	})
}

func TestCachedEditorialCandidatesDoesNotCacheAnEmptySet(t *testing.T) {
	t.Parallel()

	f := &Fetcher{
		Clock: fixedClock(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)),
	}
	calls := 0
	loader := func(context.Context, string, *int, []int, catalog.AccessFilter) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return []string{"Drama"}, nil
	}

	for range 2 {
		if _, err := f.cachedEditorialCandidates(context.Background(), "genre_roulette", nil, nil, catalog.AccessFilter{}, time.Hour, loader); err != nil {
			t.Fatalf("cachedEditorialCandidates: %v", err)
		}
	}
	got, err := f.cachedEditorialCandidates(context.Background(), "genre_roulette", nil, nil, catalog.AccessFilter{}, time.Hour, loader)
	if err != nil {
		t.Fatalf("cachedEditorialCandidates: %v", err)
	}
	if calls != 2 || len(got) != 1 || got[0] != "Drama" {
		t.Fatalf("calls = %d, candidates = %v; want the empty set reloaded once, then the cached genre", calls, got)
	}
}
