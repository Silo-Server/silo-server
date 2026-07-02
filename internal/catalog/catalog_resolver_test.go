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

func TestValidateCatalogQueryRequest_AllowsEbookMediaScope(t *testing.T) {
	req := CatalogRequest{
		Source: CatalogSourceQuery,
		Query: QueryDefinition{
			MediaScope: "ebook",
			Match:      "all",
			Sort:       QuerySort{Field: "title", Order: "asc"},
		},
	}

	if err := validateCatalogQueryRequest(req, true); err != nil {
		t.Fatalf("expected ebook media scope to be accepted, got %v", err)
	}
}

func TestValidateCatalogQueryRequest_AllowsMangaMediaScope(t *testing.T) {
	req := CatalogRequest{
		Source: CatalogSourceQuery,
		Query: QueryDefinition{
			MediaScope: "manga",
			Match:      "all",
			Sort:       QuerySort{Field: "title", Order: "asc"},
		},
	}

	if err := validateCatalogQueryRequest(req, true); err != nil {
		t.Fatalf("expected manga media scope to be accepted, got %v", err)
	}
}

func TestValidateCatalogQueryRequest_AllowsAddedAtFilter(t *testing.T) {
	req := CatalogRequest{
		Source: CatalogSourceQuery,
		Query: QueryDefinition{
			Match: "all",
			Groups: []QueryGroup{{
				Match: "all",
				Rules: []QueryRule{{Field: "added_at", Op: "in_last", Value: "1y"}},
			}},
			Sort: QuerySort{Field: "title", Order: "asc"},
		},
	}

	if err := validateCatalogQueryRequest(req, true); err != nil {
		t.Fatalf("expected added_at filter to be accepted, got %v", err)
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

func TestCatalogRuleMatchesItem_InLastDateFields(t *testing.T) {
	recentRelease := time.Now().UTC().AddDate(0, 0, -10).Format("2006-01-02")
	oldRelease := time.Now().UTC().AddDate(-2, 0, 0).Format("2006-01-02")
	recentItem := &models.MediaItem{
		CreatedAt:   time.Now().AddDate(0, 0, -10),
		ReleaseDate: &recentRelease,
	}
	oldItem := &models.MediaItem{
		CreatedAt:   time.Now().AddDate(-2, 0, 0),
		ReleaseDate: &oldRelease,
	}

	if !catalogRuleMatchesItem(recentItem, QueryRule{Field: "added_at", Op: "in_last", Value: "1y"}) {
		t.Fatal("expected recent added_at to match 1y in_last")
	}
	if catalogRuleMatchesItem(oldItem, QueryRule{Field: "added_at", Op: "in_last", Value: "1y"}) {
		t.Fatal("expected old added_at not to match 1y in_last")
	}
	if !catalogRuleMatchesItem(recentItem, QueryRule{Field: "release_date", Op: "in_last", Value: "1y"}) {
		t.Fatal("expected recent release_date to match 1y in_last")
	}
	if catalogRuleMatchesItem(oldItem, QueryRule{Field: "release_date", Op: "in_last", Value: "1y"}) {
		t.Fatal("expected old release_date not to match 1y in_last")
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

func (s *stubFacetFetcher) SearchDistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	s.hit()
	return nil, false, nil
}

func (s *stubFacetFetcher) SearchDistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	s.hit()
	return nil, false, nil
}

func (s *stubFacetFetcher) SearchPeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	s.hit()
	return nil, false, nil
}

func (s *stubFacetFetcher) SearchAudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	s.hit()
	return nil, false, nil
}

type recordingFacetFetcher struct {
	mu               sync.Mutex
	peopleKinds      []models.PersonKind
	searchPeopleKind models.PersonKind
	searchMediaScope string
}

func (s *recordingFacetFetcher) DistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return nil, nil
}

func (s *recordingFacetFetcher) DistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return nil, nil
}

func (s *recordingFacetFetcher) Resolutions(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return nil, nil
}

func (s *recordingFacetFetcher) JSONBLanguages(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return nil, nil
}

func (s *recordingFacetFetcher) SubtitleLanguages(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return nil, nil
}

func (s *recordingFacetFetcher) PeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	s.mu.Lock()
	s.peopleKinds = append(s.peopleKinds, kind)
	s.mu.Unlock()
	if kind == models.PersonKindAuthor {
		return []string{"Author"}, nil
	}
	return []string{"Narrator"}, nil
}

func (s *recordingFacetFetcher) AudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return nil, nil
}

func (s *recordingFacetFetcher) SearchDistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	return nil, false, nil
}

func (s *recordingFacetFetcher) SearchDistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	return nil, false, nil
}

func (s *recordingFacetFetcher) SearchPeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	s.mu.Lock()
	s.searchPeopleKind = kind
	s.searchMediaScope = mediaScope
	s.mu.Unlock()
	return []string{"Author"}, false, nil
}

func (s *recordingFacetFetcher) SearchAudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	return nil, false, nil
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

// fakeSearchProvider is a CatalogSearchProvider stub that returns a fixed
// CatalogSearchResult so resolveDirectSearchSource's field plumbing can be
// exercised without a database or live search backend.
type fakeSearchProvider struct {
	result *CatalogSearchResult
}

func (f *fakeSearchProvider) Search(_ context.Context, _ CatalogSearchRequest) (*CatalogSearchResult, error) {
	return f.result, nil
}

// TestResolveDirectSearchSource_PlumbsDiagnostics asserts that the four
// diagnostics fields (Provider, Mode, SemanticUsed, FallbackReason) carried by
// the provider's CatalogSearchResult are copied onto the resolver's
// CatalogResult on the direct-search path. Covers both the downgraded-to-keyword
// case (semantic_not_ready) and the hybrid-survived case.
func TestResolveDirectSearchSource_PlumbsDiagnostics(t *testing.T) {
	cases := []struct {
		name   string
		result *CatalogSearchResult
	}{
		{
			name: "keyword fallback carries reason",
			result: &CatalogSearchResult{
				Items:          []*models.MediaItem{},
				Provider:       SearchProviderMeilisearch,
				Mode:           "keyword",
				SemanticUsed:   false,
				FallbackReason: `semantic_not_ready: type "movie" coverage 40% below threshold`,
			},
		},
		{
			name: "hybrid survived",
			result: &CatalogSearchResult{
				Items:              []*models.MediaItem{},
				Provider:           SearchProviderMeilisearch,
				Mode:               "hybrid",
				SemanticUsed:       true,
				IndexPendingEvents: 7,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &CatalogResolver{
				searchProvider: &fakeSearchProvider{result: tc.result},
			}

			got, err := resolver.resolveDirectSearchSource(
				context.Background(),
				CatalogRequest{Source: CatalogSourceQuery, SearchQuery: "dune", Limit: 20},
				AccessFilter{},
			)
			if err != nil {
				t.Fatalf("resolveDirectSearchSource error: %v", err)
			}
			if got.Provider != tc.result.Provider {
				t.Fatalf("Provider = %q, want %q", got.Provider, tc.result.Provider)
			}
			if got.Mode != tc.result.Mode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tc.result.Mode)
			}
			if got.SemanticUsed != tc.result.SemanticUsed {
				t.Fatalf("SemanticUsed = %v, want %v", got.SemanticUsed, tc.result.SemanticUsed)
			}
			if got.FallbackReason != tc.result.FallbackReason {
				t.Fatalf("FallbackReason = %q, want %q", got.FallbackReason, tc.result.FallbackReason)
			}
			if got.IndexPendingEvents != tc.result.IndexPendingEvents {
				t.Fatalf("IndexPendingEvents = %d, want %d", got.IndexPendingEvents, tc.result.IndexPendingEvents)
			}
		})
	}
}

// TestResolveDirectSearchSource_EarlyEmptyOmitsDiagnostics asserts that the
// early-empty path (no accessible libraries) returns a zero-valued Provider so
// the handler omits search_diagnostics for it.
func TestResolveDirectSearchSource_EarlyEmptyOmitsDiagnostics(t *testing.T) {
	resolver := &CatalogResolver{
		searchProvider: &fakeSearchProvider{result: &CatalogSearchResult{Provider: SearchProviderMeilisearch}},
	}

	got, err := resolver.resolveDirectSearchSource(
		context.Background(),
		CatalogRequest{Source: CatalogSourceQuery, SearchQuery: "dune", Limit: 20},
		// AllowedLibraryIDs empty (non-nil) => effectiveCatalogLibraryIDs early-empties.
		AccessFilter{AllowedLibraryIDs: []int{}},
	)
	if err != nil {
		t.Fatalf("resolveDirectSearchSource error: %v", err)
	}
	if got.Provider != "" {
		t.Fatalf("early-empty Provider = %q, want empty", got.Provider)
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

func TestListFiltersWithOptions_EbookScopeSkipsNarratorFacet(t *testing.T) {
	facets := &recordingFacetFetcher{}
	resolver := &CatalogResolver{
		browseRepo: &BrowseRepository{},
		facets:     facets,
	}

	result, err := resolver.ListFiltersWithOptions(
		context.Background(),
		CatalogRequest{
			Source: CatalogSourceQuery,
			Query:  QueryDefinition{MediaScope: "ebook"},
		},
		AccessFilter{},
		CatalogFilterOptions{},
	)
	if err != nil {
		t.Fatalf("ListFiltersWithOptions returned error: %v", err)
	}

	facets.mu.Lock()
	defer facets.mu.Unlock()
	for _, kind := range facets.peopleKinds {
		if kind == models.PersonKindNarrator {
			t.Fatal("ebook filters should not request narrator facets")
		}
	}
	if len(result.Narrators) != 0 {
		t.Fatalf("ebook filters returned narrators = %v, want empty", result.Narrators)
	}
}

func TestSearchFacet_EbookScopeForwardsMediaScope(t *testing.T) {
	facets := &recordingFacetFetcher{}
	resolver := &CatalogResolver{
		browseRepo: &BrowseRepository{},
		facets:     facets,
	}

	result, err := resolver.SearchFacet(
		context.Background(),
		CatalogRequest{
			Source: CatalogSourceQuery,
			Query:  QueryDefinition{MediaScope: "ebook"},
		},
		AccessFilter{},
		"author",
		"Au",
		20,
	)
	if err != nil {
		t.Fatalf("SearchFacet returned error: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0] != "Author" {
		t.Fatalf("matches = %v, want Author", result.Matches)
	}

	facets.mu.Lock()
	defer facets.mu.Unlock()
	if facets.searchPeopleKind != models.PersonKindAuthor {
		t.Fatalf("searchPeopleKind = %v, want author", facets.searchPeopleKind)
	}
	if facets.searchMediaScope != "ebook" {
		t.Fatalf("search mediaScope = %q, want ebook", facets.searchMediaScope)
	}
}

func TestSearchFacet_EbookScopeRejectsNarratorFacet(t *testing.T) {
	resolver := &CatalogResolver{
		browseRepo: &BrowseRepository{},
		facets:     &recordingFacetFetcher{},
	}

	_, err := resolver.SearchFacet(
		context.Background(),
		CatalogRequest{
			Source: CatalogSourceQuery,
			Query:  QueryDefinition{MediaScope: "ebook"},
		},
		AccessFilter{},
		"narrator",
		"Na",
		20,
	)
	if err == nil {
		t.Fatal("expected narrator facet to be rejected for ebook scope")
	}
}
