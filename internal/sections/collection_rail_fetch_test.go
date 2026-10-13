package sections

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCollectionRailItemsToFetchPreservesLegacyBoundWithoutDefaultSort(t *testing.T) {
	items := make([]*models.LibraryCollectionItem, 100_000)

	bounded := collectionRailItemsToFetch(items, 24)
	if len(bounded) != 24 {
		t.Fatalf("unsorted rail selected %d items, want 24", len(bounded))
	}
	visible, total := unsortedCollectionRailResult(make([]*models.MediaItem, len(bounded)))
	if len(visible) != 24 || total != 24 {
		t.Fatalf("unsorted rail result = %d items, total %d; want 24/24", len(visible), total)
	}
}

func TestCollectionRailQueryAccessIntersectsExplicitLibrary(t *testing.T) {
	requestedLibraryID := 7

	matching := collectionRailQueryAccess(
		catalog.AccessFilter{AllowedLibraryIDs: []int{4, 7}},
		&requestedLibraryID,
		nil,
	)
	if len(matching.AllowedLibraryIDs) != 1 || matching.AllowedLibraryIDs[0] != requestedLibraryID {
		t.Fatalf("matching access = %v, want [%d]", matching.AllowedLibraryIDs, requestedLibraryID)
	}

	blocked := collectionRailQueryAccess(
		catalog.AccessFilter{AllowedLibraryIDs: []int{4}},
		&requestedLibraryID,
		nil,
	)
	if blocked.AllowedLibraryIDs == nil || len(blocked.AllowedLibraryIDs) != 0 {
		t.Fatalf("blocked access = %v, want non-nil empty scope", blocked.AllowedLibraryIDs)
	}
}

func TestCollectionRailQueryAccessKeepsEmptyScopeDenying(t *testing.T) {
	got := collectionRailQueryAccess(catalog.AccessFilter{AllowedLibraryIDs: []int{}}, nil, []int{})
	if got.AllowedLibraryIDs == nil || len(got.AllowedLibraryIDs) != 0 {
		t.Fatalf("empty scope = %#v, want non-nil empty scope", got.AllowedLibraryIDs)
	}
}

func TestNarrowLibraryScopeNeverWidensAllowedLibraries(t *testing.T) {
	tests := []struct {
		name           string
		scope, allowed []int
		want           []int
	}{
		{name: "unrestricted", scope: nil, allowed: nil, want: nil},
		{name: "scope only", scope: []int{1, 2}, allowed: nil, want: []int{1, 2}},
		{name: "allowed only", scope: nil, allowed: []int{2}, want: []int{2}},
		{name: "overlap", scope: []int{1, 2}, allowed: []int{2, 3}, want: []int{2}},
		{name: "disjoint", scope: []int{1}, allowed: []int{2}, want: []int{}},
		{name: "nothing allowed", scope: []int{1}, allowed: []int{}, want: []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := narrowLibraryScope(tt.scope, tt.allowed)
			if (got == nil) != (tt.want == nil) || !slices.Equal(got, tt.want) {
				t.Fatalf("narrowLibraryScope(%#v, %#v) = %#v, want %#v", tt.scope, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestApplySectionLibraryScopeToQueryRejectsDisjointScope(t *testing.T) {
	library := 9
	tests := []struct {
		name       string
		query      []int
		libraryID  *int
		libraryIDs []int
		want       []int
		ok         bool
	}{
		{name: "no scope", query: []int{1}, want: []int{1}, ok: true},
		{name: "scope only", libraryIDs: []int{1, 2}, want: []int{1, 2}, ok: true},
		{name: "overlap", query: []int{1, 2}, libraryIDs: []int{2, 3}, want: []int{2}, ok: true},
		{name: "disjoint", query: []int{1}, libraryIDs: []int{2}, ok: false},
		{name: "empty scope", query: []int{1}, libraryIDs: []int{}, ok: false},
		{name: "empty scope, unscoped query", libraryIDs: []int{}, ok: false},
		{name: "library page", query: []int{1}, libraryID: &library, want: []int{9}, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := applySectionLibraryScopeToQuery(catalog.QueryDefinition{LibraryIDs: tt.query}, tt.libraryID, tt.libraryIDs)
			if ok != tt.ok {
				t.Fatalf("ok = %t, want %t", ok, tt.ok)
			}
			if ok && !slices.Equal(got.LibraryIDs, tt.want) {
				t.Fatalf("library_ids = %v, want %v", got.LibraryIDs, tt.want)
			}
		})
	}
}
