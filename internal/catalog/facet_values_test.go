package catalog

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func listOf(n int) *facetValueList {
	values := make([]FacetValue, n)
	for i := range values {
		values[i] = FacetValue{Value: string(rune('a' + i)), Count: 1}
	}
	return newFacetValueList(values)
}

func staticLoad(l *facetValueList, calls *atomic.Int32) func(context.Context) (*facetValueList, error) {
	return func(context.Context) (*facetValueList, error) {
		calls.Add(1)
		return l, nil
	}
}

func TestFacetValueCacheKeySeparatesScopes(t *testing.T) {
	query := "SELECT name, n FROM x WHERE mi.media_folder_id = ANY($1)"
	a, okA := queryCacheKey(query, []any{[]int{1}})
	b, okB := queryCacheKey(query, []any{[]int{2}})
	a2, _ := queryCacheKey(query, []any{[]int{1}})
	other, _ := queryCacheKey(query+" AND TRUE", []any{[]int{1}})
	if !okA || !okB || a == b || a != a2 || a == other {
		t.Fatalf("keys: a=%x b=%x a2=%x other=%x", a, b, a2, other)
	}
	if _, ok := queryCacheKey(query, []any{make(chan int)}); ok {
		t.Fatal("an argument that cannot be encoded must make the scope uncacheable")
	}

	c := newFacetValueCache(time.Minute, 100)
	var calls atomic.Int32
	gotA, _ := c.get(t.Context(), a, staticLoad(listOf(1), &calls))
	gotB, _ := c.get(t.Context(), b, staticLoad(listOf(2), &calls))
	if calls.Load() != 2 || len(gotA.entries) != 1 || len(gotB.entries) != 2 {
		t.Fatalf("scopes shared a list: calls=%d a=%d b=%d", calls.Load(), len(gotA.entries), len(gotB.entries))
	}
}

func TestFacetValueCacheSingleflight(t *testing.T) {
	c := newFacetValueCache(time.Minute, 100)
	const callers = 8
	var calls atomic.Int32
	var started sync.WaitGroup
	started.Add(callers)
	release := make(chan struct{})
	load := func(context.Context) (*facetValueList, error) {
		calls.Add(1)
		<-release
		return listOf(3), nil
	}
	var wg sync.WaitGroup
	results := make([]*facetValueList, callers)
	for i := range callers {
		wg.Go(func() {
			started.Done()
			l, err := c.get(t.Context(), "k", load)
			if err != nil {
				t.Error(err)
			}
			results[i] = l
		})
	}
	started.Wait()
	// Callers that miss the cache before the load lands join its flight;
	// later ones find the stored list. Either way one load serves them all.
	for calls.Load() == 0 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loads = %d, want 1", calls.Load())
	}
	for i, l := range results {
		if l != results[0] {
			t.Fatalf("caller %d got a different list", i)
		}
	}
}

func TestFacetValueCacheCallerCancelDoesNotStopTheBuild(t *testing.T) {
	c := newFacetValueCache(time.Minute, 100)
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	load := func(ctx context.Context) (*facetValueList, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return listOf(3), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	callerCtx, cancel := context.WithCancel(t.Context())
	callerErr := make(chan error, 1)
	go func() {
		_, err := c.get(callerCtx, "k", load)
		callerErr <- err
	}()
	<-entered
	cancel()
	if err := <-callerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller err = %v, want context.Canceled", err)
	}
	// The next keystroke joins the build the canceled one started.
	next := make(chan *facetValueList, 1)
	go func() {
		l, err := c.get(t.Context(), "k", load)
		if err != nil {
			t.Error(err)
		}
		next <- l
	}()
	close(release)
	if l := <-next; l == nil || len(l.entries) != 3 || calls.Load() != 1 {
		t.Fatalf("next caller got %+v after %d loads, want the one full list", l, calls.Load())
	}
}

func TestFacetValueCacheTimedOutLoadIsNotCached(t *testing.T) {
	c := newFacetValueCache(time.Minute, 100)
	c.loadTimeout = 10 * time.Millisecond
	var calls atomic.Int32
	load := func(ctx context.Context) (*facetValueList, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			// A canceled scan hands back whatever rows it read; it must
			// not reach the cache.
			return &facetValueList{entries: listOf(1).entries}, ctx.Err()
		}
		return listOf(3), nil
	}
	if _, err := c.get(t.Context(), "k", load); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the load's deadline", err)
	}
	l, err := c.get(t.Context(), "k", load)
	if err != nil || len(l.entries) != 3 || calls.Load() != 2 {
		t.Fatalf("list = %+v err=%v calls=%d", l, err, calls.Load())
	}
}

func TestFacetValueCachePanickingLoadFailsTheFlight(t *testing.T) {
	c := newFacetValueCache(time.Minute, 100)
	_, err := c.get(t.Context(), "k", func(context.Context) (*facetValueList, error) { panic("boom") })
	if err == nil || len(c.entries) != 0 || len(c.fills) != 0 {
		t.Fatalf("err = %v entries=%d fills=%d", err, len(c.entries), len(c.fills))
	}
	if l, err := c.get(t.Context(), "k", func(context.Context) (*facetValueList, error) { return listOf(2), nil }); err != nil || len(l.entries) != 2 {
		t.Fatalf("after a panic: list=%+v err=%v", l, err)
	}
}

// Misses for different scopes build at most cap(fills) lists at once; a
// build that cannot get a slot within the load timeout fails uncached.
func TestFacetValueCacheBoundsConcurrentFills(t *testing.T) {
	c := newFacetValueCache(time.Minute, 100)
	c.fills = make(chan struct{}, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := c.get(t.Context(), "a", func(context.Context) (*facetValueList, error) {
			close(entered)
			<-release
			return listOf(1), nil
		})
		first <- err
	}()
	<-entered

	c.loadTimeout = 20 * time.Millisecond
	var calls atomic.Int32
	load := func(context.Context) (*facetValueList, error) {
		calls.Add(1)
		return listOf(3), nil
	}
	if _, err := c.get(t.Context(), "b", load); !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatalf("second scope: err=%v loads=%d, want a deadline before its load ran", err, calls.Load())
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	c.loadTimeout = time.Minute
	if l, err := c.get(t.Context(), "b", load); err != nil || len(l.entries) != 3 || calls.Load() != 1 {
		t.Fatalf("after the slot freed: list=%+v err=%v loads=%d", l, err, calls.Load())
	}
}

func TestFacetValueCacheLoadErrorReachesWaitersAndIsNotCached(t *testing.T) {
	c := newFacetValueCache(time.Minute, 100)
	boom := errors.New("boom")
	var calls atomic.Int32
	load := func(context.Context) (*facetValueList, error) {
		calls.Add(1)
		return nil, boom
	}
	if _, err := c.get(t.Context(), "k", load); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.get(t.Context(), "k", load); !errors.Is(err, boom) || calls.Load() != 2 {
		t.Fatalf("err = %v calls=%d; a failure must not be cached", err, calls.Load())
	}
}

// columnRecordingFetcher records the column searches SearchFacet makes.
type columnRecordingFetcher struct {
	recordingFacetFetcher
	searches []facetSearch
	columns  []facetColumn
	filters  []BrowseFilters
}

func (f *columnRecordingFetcher) SearchColumnValues(_ context.Context, column facetColumn, filters BrowseFilters, _ string, _ string, search facetSearch) ([]FacetValue, bool, error) {
	f.columns = append(f.columns, column)
	f.searches = append(f.searches, search)
	f.filters = append(f.filters, filters)
	return []FacetValue{{Value: "Drama", Count: 3}}, true, nil
}

// matches keeps the prefix answer every client reads, A-Z and nothing for
// an empty q; values carries the ranked answer.
func TestSearchFacetAnswersMatchesAndValues(t *testing.T) {
	facets := &columnRecordingFetcher{}
	resolver := &CatalogResolver{browseRepo: &BrowseRepository{}, facets: facets}
	req := CatalogRequest{Source: CatalogSourceQuery}

	got, err := resolver.SearchFacet(t.Context(), req, AccessFilter{}, "genre", "  ", 500)
	if err != nil {
		t.Fatal(err)
	}
	if got.Matches == nil || len(got.Matches) != 0 || got.HasMore {
		t.Fatalf("empty q matches = %v hasMore=%v, want none", got.Matches, got.HasMore)
	}
	if !got.ValuesHasMore || len(got.Values) != 1 || len(facets.searches) != 1 {
		t.Fatalf("empty q values = %+v, searches %v", got, facets.searches)
	}
	if s := facets.searches[0]; s.Q != "" || s.Limit != catalogFacetSearchMaxLimit || s.Mode != facetSearchRanked || facets.columns[0] != facetColumns[facetGenre] {
		t.Fatalf("search = %+v column = %+v", s, facets.columns[0])
	}

	facets.searches = nil
	got, err = resolver.SearchFacet(t.Context(), req, AccessFilter{}, "genre", " dr ", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []facetSearch{{Q: "dr", Limit: 10, Mode: facetSearchPrefix}, {Q: "dr", Limit: 10, Mode: facetSearchRanked}}
	if !slices.Equal(facets.searches, want) {
		t.Fatalf("searches = %+v, want %+v", facets.searches, want)
	}
	if !slices.Equal(got.Matches, []string{"Drama"}) || !got.HasMore || len(got.Values) != 1 || !got.ValuesHasMore {
		t.Fatalf("q=dr answer = %+v", got)
	}

	facets.searches = nil
	for _, facet := range []string{"author", "series"} {
		got, err := resolver.SearchFacet(t.Context(), req, AccessFilter{}, facet, "", 10)
		if err != nil || got.Matches == nil || len(got.Matches) != 0 || got.Values == nil || len(got.Values) != 0 {
			t.Fatalf("%s: got %+v err %v, want empty lists", facet, got, err)
		}
	}
	if len(facets.searches) != 0 {
		t.Fatalf("empty q reached the fetcher: %v", facets.searches)
	}
}
