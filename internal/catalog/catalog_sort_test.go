package catalog

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func ptr(s string) *string { return &s }

func TestSortCatalogItems_LastAirDateDescending(t *testing.T) {
	items := []*models.MediaItem{
		{ContentID: "old", Title: "Old Show", LastAirDate: ptr("2020-01-15")},
		{ContentID: "new", Title: "New Show", LastAirDate: ptr("2024-02-22")},
		{ContentID: "undated", Title: "Undated Show", LastAirDate: nil},
		{ContentID: "mid", Title: "Mid Show", LastAirDate: ptr("2024-06-01")},
	}

	sortCatalogItems(items, QuerySort{Field: "last_air_date", Order: "desc"})

	expected := []string{"mid", "new", "old", "undated"}
	for i, id := range expected {
		if items[i].ContentID != id {
			t.Fatalf("position %d: expected %q, got %q", i, id, items[i].ContentID)
		}
	}
}
