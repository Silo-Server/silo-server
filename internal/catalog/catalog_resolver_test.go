package catalog

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCatalogScopedLibraryRequestPreservesGlobalDisabledLibraries(t *testing.T) {
	req := CatalogRequest{Query: QueryDefinition{LibraryIDs: []int{7}}}
	viewerAccess := AccessFilter{DisabledLibraryIDs: []int{9}}

	searchAccess, _, earlyEmpty := catalogSearchAccess(req, viewerAccess)
	if earlyEmpty {
		t.Fatal("catalogSearchAccess returned early empty for an enabled requested library")
	}
	if !slices.Equal(searchAccess.AllowedLibraryIDs, []int{7}) {
		t.Fatalf("search allowed libraries = %v, want [7]", searchAccess.AllowedLibraryIDs)
	}
	if !slices.Equal(searchAccess.DisabledLibraryIDs, []int{9}) {
		t.Fatalf("search disabled libraries = %v, want [9]", searchAccess.DisabledLibraryIDs)
	}

	browseFilters, earlyEmpty, err := catalogBrowseFilters(req, viewerAccess)
	if err != nil {
		t.Fatalf("catalogBrowseFilters: %v", err)
	}
	if earlyEmpty {
		t.Fatal("catalogBrowseFilters returned early empty for an enabled requested library")
	}
	if browseFilters.LibraryID != 7 {
		t.Fatalf("browse library = %d, want 7", browseFilters.LibraryID)
	}
	if !slices.Equal(browseFilters.DisabledLibraryIDs, []int{9}) {
		t.Fatalf("browse disabled libraries = %v, want [9]", browseFilters.DisabledLibraryIDs)
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

func (s *recordingFacetFetcher) SearchColumnValues(ctx context.Context, column facetColumn, filters BrowseFilters, baseRelation string, mediaScope string, search facetSearch) ([]FacetValue, bool, error) {
	return nil, false, nil
}

func (s *recordingFacetFetcher) SearchPeopleByKind(ctx context.Context, kind models.PersonKind, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]FacetValue, bool, error) {
	s.mu.Lock()
	s.searchPeopleKind = kind
	s.searchMediaScope = mediaScope
	s.mu.Unlock()
	return []FacetValue{{Value: "Author", Count: 1}}, false, nil
}

func (s *recordingFacetFetcher) SearchAudiobookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]FacetValue, bool, error) {
	return nil, false, nil
}

// fakeSearchProvider is a CatalogSearchProvider stub that returns a fixed
// CatalogSearchResult so resolveDirectSearchSource's field plumbing can be
// exercised without a database or live search backend.
type fakeSearchProvider struct {
	result   *CatalogSearchResult
	requests []CatalogSearchRequest
}

func (f *fakeSearchProvider) Search(_ context.Context, req CatalogSearchRequest) (*CatalogSearchResult, error) {
	f.requests = append(f.requests, req)
	return f.result, nil
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
		// AllowedLibraryIDs empty (non-nil) => AccessFilter.LibraryScope early-empties.
		AccessFilter{AllowedLibraryIDs: []int{}},
	)
	if err != nil {
		t.Fatalf("resolveDirectSearchSource error: %v", err)
	}
	if got.Provider != "" {
		t.Fatalf("early-empty Provider = %q, want empty", got.Provider)
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
