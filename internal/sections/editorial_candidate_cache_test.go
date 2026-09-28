package sections

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

func TestCachedEditorialCandidatesReusesCandidateListForSameScope(t *testing.T) {
	t.Parallel()

	f := &Fetcher{
		Clock: recipes.FixedClock(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)),
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
		Clock: recipes.FixedClock(now),
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

	f.Clock = recipes.FixedClock(now.Add(2 * time.Hour))
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
		Clock: recipes.FixedClock(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)),
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
		Clock: recipes.FixedClock(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)),
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
