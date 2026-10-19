package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBoxSetUnavailableCount(t *testing.T) {
	h := newCollectionsTestHandler(&fakeCollectionSource{}, nil, nil)
	for _, tc := range []struct {
		status      string
		count, want int
	}{
		{"unavailable", 99, 0},
		{"future-status", 99, 0},
		{"available", 0, 0},
		{"available", 2, 2},
		{"", 2, 2},
	} {
		c := &models.LibraryCollection{ID: "synthetic", Title: "Synthetic collection", ItemCount: tc.count, ItemCountStatus: tc.status}
		dto := h.boxSetFromCollection(t.Context(), c, catalog.CollectionPoster{})
		if dto.ChildCount != tc.want || dto.RecursiveItemCount != tc.want {
			t.Fatalf("%q/%d -> %d/%d, want %d", tc.status, tc.count, dto.ChildCount, dto.RecursiveItemCount, tc.want)
		}
		if dto.Name != c.Title || dto.ID == "" {
			t.Fatal("unavailable count hid the collection")
		}
	}
}
