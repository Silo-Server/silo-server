package sections

import (
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
