package catalog

import (
	"context"
	"slices"
	"sync"
	"testing"

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

func TestCatalogSearchAccessPreservesAccountID(t *testing.T) {
	searchAccess, _, earlyEmpty := catalogSearchAccess(CatalogRequest{}, AccessFilter{UserID: 7})
	if earlyEmpty {
		t.Fatal("catalogSearchAccess returned early empty")
	}
	if searchAccess.UserID != 7 {
		t.Fatalf("search account = %d, want 7", searchAccess.UserID)
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
