package catalog

import (
	"context"
	"slices"
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

func TestFilterCatalogItems_NormalizesMediaScope(t *testing.T) {
	items := []*models.MediaItem{
		{ContentID: "book", Type: "ebook"},
		{ContentID: "movie", Type: "movie"},
	}

	filtered := filterCatalogItems(items, QueryDefinition{MediaScope: " Ebook "})
	if len(filtered) != 1 || filtered[0].ContentID != "book" {
		t.Fatalf("expected normalized ebook scope to keep only ebook item, got %#v", filtered)
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

func (s *stubFacetFetcher) EbookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
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

func (s *stubFacetFetcher) SearchEbookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	s.hit()
	return nil, false, nil
}

type recordingFacetFetcher struct {
	peopleKinds     []models.PersonKind
	searchKinds     []models.PersonKind
	audiobookSeries int
	ebookSeries     int
	searchAudiobook int
	searchEbook     int
	lastMediaScope  string
}

func (r *recordingFacetFetcher) DistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	r.lastMediaScope = mediaScope
	return nil, nil
}

func (r *recordingFacetFetcher) DistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	r.lastMediaScope = mediaScope
	return nil, nil
}

func (r *recordingFacetFetcher) Resolutions(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	r.lastMediaScope = mediaScope
	return nil, nil
}

func (r *recordingFacetFetcher) JSONBLanguages(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	r.lastMediaScope = mediaScope
	return nil, nil
}

func (r *recordingFacetFetcher) SubtitleLanguages(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	r.lastMediaScope = mediaScope
	return nil, nil
}

func (r *recordingFacetFetcher) PeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	r.lastMediaScope = mediaScope
	r.peopleKinds = append(r.peopleKinds, kind)
	if kind == models.PersonKindAuthor {
		return []string{"Octavia Butler"}, nil
	}
	if kind == models.PersonKindNarrator {
		return []string{"Should Not Load"}, nil
	}
	return nil, nil
}

func (r *recordingFacetFetcher) AudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	r.lastMediaScope = mediaScope
	r.audiobookSeries++
	return []string{"Audio Series"}, nil
}

func (r *recordingFacetFetcher) EbookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	r.lastMediaScope = mediaScope
	r.ebookSeries++
	return []string{"Ebook Series"}, nil
}

func (r *recordingFacetFetcher) SearchDistinctArrayColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	r.lastMediaScope = mediaScope
	return nil, false, nil
}

func (r *recordingFacetFetcher) SearchDistinctScalarColumn(ctx context.Context, column string, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	r.lastMediaScope = mediaScope
	return nil, false, nil
}

func (r *recordingFacetFetcher) SearchPeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	r.lastMediaScope = mediaScope
	r.searchKinds = append(r.searchKinds, kind)
	if kind == models.PersonKindNarrator {
		return []string{"Audio Narrator"}, true, nil
	}
	return nil, false, nil
}

func (r *recordingFacetFetcher) SearchAudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	r.lastMediaScope = mediaScope
	r.searchAudiobook++
	return []string{"Audio Search"}, false, nil
}

func (r *recordingFacetFetcher) SearchEbookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error) {
	r.lastMediaScope = mediaScope
	r.searchEbook++
	return []string{"Ebook Search"}, true, nil
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

func TestListFiltersWithOptions_EbookScopeUsesAuthorsAndEbookSeriesWithoutNarrators(t *testing.T) {
	facets := &recordingFacetFetcher{}
	resolver := &CatalogResolver{
		browseRepo: &BrowseRepository{},
		facets:     facets,
	}

	result, err := resolver.ListFiltersWithOptions(
		context.Background(),
		CatalogRequest{
			Source: CatalogSourceQuery,
			Query: QueryDefinition{
				MediaScope: "ebook",
				Match:      "all",
				Sort:       QuerySort{Field: "title", Order: "asc"},
			},
		},
		AccessFilter{},
		CatalogFilterOptions{},
	)
	if err != nil {
		t.Fatalf("ListFiltersWithOptions returned error: %v", err)
	}
	if facets.lastMediaScope != "ebook" {
		t.Fatalf("expected facet media scope ebook, got %q", facets.lastMediaScope)
	}
	if !slices.Contains(facets.peopleKinds, models.PersonKindAuthor) {
		t.Fatalf("expected author facet to be loaded, got kinds %v", facets.peopleKinds)
	}
	if slices.Contains(facets.peopleKinds, models.PersonKindNarrator) {
		t.Fatalf("expected narrator facet not to be loaded for ebook scope, got kinds %v", facets.peopleKinds)
	}
	if facets.ebookSeries != 1 || facets.audiobookSeries != 0 {
		t.Fatalf("expected ebook series only, got ebook=%d audiobook=%d", facets.ebookSeries, facets.audiobookSeries)
	}
	if len(result.Authors) != 1 || result.Authors[0] != "Octavia Butler" {
		t.Fatalf("expected ebook authors in result, got %v", result.Authors)
	}
	if len(result.Narrators) != 0 {
		t.Fatalf("expected no ebook narrators in result, got %v", result.Narrators)
	}
	if len(result.Series) != 1 || result.Series[0] != "Ebook Series" {
		t.Fatalf("expected ebook series in result, got %v", result.Series)
	}
}

func TestListFiltersWithOptions_NormalizesEbookScopeBeforeFacetDispatch(t *testing.T) {
	facets := &recordingFacetFetcher{}
	resolver := &CatalogResolver{
		browseRepo: &BrowseRepository{},
		facets:     facets,
	}

	result, err := resolver.ListFiltersWithOptions(
		context.Background(),
		CatalogRequest{
			Source: CatalogSourceQuery,
			Query: QueryDefinition{
				MediaScope: " Ebook ",
				Match:      "all",
				Sort:       QuerySort{Field: "title", Order: "asc"},
			},
		},
		AccessFilter{},
		CatalogFilterOptions{},
	)
	if err != nil {
		t.Fatalf("ListFiltersWithOptions returned error: %v", err)
	}
	if facets.lastMediaScope != "ebook" {
		t.Fatalf("expected normalized facet media scope ebook, got %q", facets.lastMediaScope)
	}
	if slices.Contains(facets.peopleKinds, models.PersonKindNarrator) {
		t.Fatalf("expected narrator facet not to be loaded for normalized ebook scope, got kinds %v", facets.peopleKinds)
	}
	if len(result.Narrators) != 0 {
		t.Fatalf("expected no ebook narrators in result, got %v", result.Narrators)
	}
}

func TestDispatchFacetSearch_EbookSeriesUsesEbookDetails(t *testing.T) {
	facets := &recordingFacetFetcher{}
	matches, hasMore, err := dispatchFacetSearch(
		context.Background(),
		facets,
		"series",
		BrowseFilters{},
		"media_items mi",
		"ebook",
		"Dune",
		10,
	)
	if err != nil {
		t.Fatalf("dispatchFacetSearch returned error: %v", err)
	}
	if facets.searchEbook != 1 || facets.searchAudiobook != 0 {
		t.Fatalf("expected ebook series search only, got ebook=%d audiobook=%d", facets.searchEbook, facets.searchAudiobook)
	}
	if !hasMore || len(matches) != 1 || matches[0] != "Ebook Search" {
		t.Fatalf("expected ebook series search result with hasMore, got matches=%v hasMore=%v", matches, hasMore)
	}
}

func TestDispatchFacetSearch_EbookNarratorReturnsEmptyWithoutLookup(t *testing.T) {
	facets := &recordingFacetFetcher{}
	matches, hasMore, err := dispatchFacetSearch(
		context.Background(),
		facets,
		"narrator",
		BrowseFilters{},
		"media_items mi",
		"ebook",
		"read",
		10,
	)
	if err != nil {
		t.Fatalf("dispatchFacetSearch returned error: %v", err)
	}
	if len(facets.searchKinds) != 0 {
		t.Fatalf("expected no people lookup for ebook narrator search, got kinds %v", facets.searchKinds)
	}
	if hasMore || len(matches) != 0 {
		t.Fatalf("expected empty ebook narrator result, got matches=%v hasMore=%v", matches, hasMore)
	}
}

func TestDispatchFacetSearch_AudiobookNarratorUsesNarratorLookup(t *testing.T) {
	facets := &recordingFacetFetcher{}
	matches, hasMore, err := dispatchFacetSearch(
		context.Background(),
		facets,
		"narrator",
		BrowseFilters{},
		"media_items mi",
		"audiobook",
		"read",
		10,
	)
	if err != nil {
		t.Fatalf("dispatchFacetSearch returned error: %v", err)
	}
	if !slices.Contains(facets.searchKinds, models.PersonKindNarrator) {
		t.Fatalf("expected narrator lookup for audiobook scope, got kinds %v", facets.searchKinds)
	}
	if !hasMore || len(matches) != 1 || matches[0] != "Audio Narrator" {
		t.Fatalf("expected audiobook narrator result with hasMore, got matches=%v hasMore=%v", matches, hasMore)
	}
}
