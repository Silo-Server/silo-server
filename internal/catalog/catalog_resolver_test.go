package catalog

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestValidateCatalogQueryRequest_AllowsLastAirDateSort(t *testing.T) {
	req := CatalogRequest{
		Source: CatalogSourceQuery,
		Query: QueryDefinition{
			Match: "all",
			Sort:  QuerySort{Field: "last_air_date", Order: "desc"},
		},
	}

	if err := validateCatalogQueryRequest(req, false); err != nil {
		t.Fatalf("expected last_air_date sort to be accepted, got %v", err)
	}
}

func TestValidateCatalogQueryRequest_AllowsEpisodeMediaScope(t *testing.T) {
	req := CatalogRequest{
		Source: CatalogSourceQuery,
		Query: QueryDefinition{
			MediaScope: "episode",
			Match:      "all",
			Sort:       QuerySort{Field: "title", Order: "asc"},
		},
	}

	if err := validateCatalogQueryRequest(req, true); err != nil {
		t.Fatalf("expected episode media scope to be accepted, got %v", err)
	}
}

func TestValidateCatalogQueryRequest_RejectsPersonalizedSortWithoutProfileScopedSurface(t *testing.T) {
	req := CatalogRequest{
		Source: CatalogSourceQuery,
		Query: QueryDefinition{
			Match: "all",
			Sort:  QuerySort{Field: "progress", Order: "desc"},
		},
	}

	if err := validateCatalogQueryRequest(req, false); err == nil {
		t.Fatal("expected progress sort to be rejected for shared query sources")
	}
}

func TestValidateCatalogQueryRequest_AllowsPersonalizedSortWithProfileScopedSurface(t *testing.T) {
	req := CatalogRequest{
		Source: CatalogSourceQuery,
		Query: QueryDefinition{
			Match: "all",
			Sort:  QuerySort{Field: "progress", Order: "desc"},
		},
	}

	if err := validateCatalogQueryRequest(req, true); err != nil {
		t.Fatalf("expected progress sort to be accepted for profile-scoped query source, got %v", err)
	}
}

// concurrentCallBarrier records the maximum number of callers observed
// in-flight at the same time. Each call into the stubbed facet fetcher invokes
// hit, which:
//
//  1. increments an in-flight counter and records a new maximum if applicable;
//  2. blocks until the configured target of concurrent callers has arrived (or
//     a safety timeout fires) so the maxSeen counter has a chance to climb to
//     the target before any caller returns;
//  3. decrements the in-flight counter on the way out.
//
// assertConcurrent fails the test if fewer than target callers were ever
// in-flight simultaneously.
type concurrentCallBarrier struct {
	t        *testing.T
	target   int32
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	releaseC chan struct{}
	once     sync.Once
}

func newConcurrentCallBarrier(t *testing.T, target int) *concurrentCallBarrier {
	t.Helper()
	return &concurrentCallBarrier{
		t:        t,
		target:   int32(target),
		releaseC: make(chan struct{}),
	}
}

func (b *concurrentCallBarrier) markReleased() {
	b.once.Do(func() {
		close(b.releaseC)
	})
}

// hit is invoked from inside a stubbed facet call.
func (b *concurrentCallBarrier) hit() {
	current := b.inFlight.Add(1)
	defer b.inFlight.Add(-1)

	for {
		prev := b.maxSeen.Load()
		if current <= prev || b.maxSeen.CompareAndSwap(prev, current) {
			break
		}
	}

	if current >= b.target {
		// Once at least `target` callers are in-flight, release every blocked
		// caller (including this one) so they can return.
		b.markReleased()
		return
	}

	// Wait for the target to be reached, or for a safety timeout. The timeout
	// keeps the test from hanging if parallelism is broken.
	select {
	case <-b.releaseC:
	case <-time.After(2 * time.Second):
		// Safety net so a regression doesn't deadlock the test forever.
		b.markReleased()
	}
}

func (b *concurrentCallBarrier) assertConcurrent(t *testing.T) {
	t.Helper()
	if got := b.maxSeen.Load(); got < b.target {
		t.Fatalf("expected at least %d concurrent facet callers, observed max %d", b.target, got)
	}
}

// stubFacetFetcher implements facetFetcher and routes every call through a
// barrier hit so a test can observe parallelism.
type stubFacetFetcher struct {
	hit func()
}

func (s *stubFacetFetcher) DistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	s.hit()
	return nil, nil
}

func (s *stubFacetFetcher) DistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	s.hit()
	return nil, nil
}

func (s *stubFacetFetcher) Resolutions(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	s.hit()
	return nil, nil
}

func (s *stubFacetFetcher) JSONBLanguages(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	s.hit()
	return nil, nil
}

func (s *stubFacetFetcher) SubtitleLanguages(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	s.hit()
	return nil, nil
}

func (s *stubFacetFetcher) PeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	s.hit()
	return nil, nil
}

func (s *stubFacetFetcher) AudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	s.hit()
	return nil, nil
}

// countingExecutor is a previewExecutor stub that records the number of
// PreviewPage calls and the AccessFilter passed in (so tests can confirm
// NamePrefix made it through). It returns an empty result set so the resolver
// path under test can run without a database.
type countingExecutor struct {
	previewCalls   int
	lastNamePrefix string
	lastLimit      int
}

func (c *countingExecutor) PreviewPage(
	_ context.Context,
	_ QueryDefinition,
	access AccessFilter,
	limit int,
	_ int,
	_ bool,
) ([]*models.MediaItem, int, bool, error) {
	c.previewCalls++
	c.lastNamePrefix = access.NamePrefix
	c.lastLimit = limit
	return []*models.MediaItem{}, 0, false, nil
}

// newTestResolver wires the supplied previewExecutor into a resolver via the
// test seam so previewQuerySource can be exercised without a database.
func newTestResolver(exec previewExecutor) *CatalogResolver {
	return &CatalogResolver{
		previewExecutorForScope: func(scope string, snapshot *time.Time) previewExecutor {
			return exec
		},
	}
}

// TestPreviewQuerySource_NamePrefix_DoesNotFetchAllRows asserts that the
// preview path makes a single PreviewPage call with NamePrefix forwarded into
// AccessFilter, instead of the previous fetch-all + Go-side filter pattern
// that called Preview twice (count + full set).
func TestPreviewQuerySource_NamePrefix_DoesNotFetchAllRows(t *testing.T) {
	exec := &countingExecutor{}
	resolver := newTestResolver(exec)

	_, err := resolver.previewQuerySource(context.Background(),
		CatalogRequest{NamePrefix: "T", Limit: 20}, AccessFilter{})
	if err != nil {
		t.Fatalf("previewQuerySource error: %v", err)
	}
	if exec.previewCalls != 1 {
		t.Fatalf("expected 1 PreviewPage call; got %d", exec.previewCalls)
	}
	if exec.lastNamePrefix != "T" {
		t.Fatalf("expected NamePrefix=%q to be forwarded into AccessFilter; got %q", "T", exec.lastNamePrefix)
	}
	if exec.lastLimit != 20 {
		t.Fatalf("expected limit=20 to reach the executor; got %d", exec.lastLimit)
	}
}

// TestListFiltersWithOptions_RunsFacetQueriesConcurrently asserts that the
// resolver runs the facet lookups in parallel rather than serializing them.
// We expect at least 6 concurrent in-flight callers (the configured cap).
func TestListFiltersWithOptions_RunsFacetQueriesConcurrently(t *testing.T) {
	const expectedConcurrent = 6
	barrier := newConcurrentCallBarrier(t, expectedConcurrent)

	resolver := &CatalogResolver{
		browseRepo: &BrowseRepository{},
		facets:     &stubFacetFetcher{hit: barrier.hit},
	}

	req := CatalogRequest{Source: CatalogSourceQuery}

	if _, err := resolver.ListFiltersWithOptions(
		context.Background(),
		req,
		AccessFilter{},
		CatalogFilterOptions{IncludeTechnical: true},
	); err != nil {
		t.Fatalf("ListFiltersWithOptions returned error: %v", err)
	}

	barrier.assertConcurrent(t)
}
